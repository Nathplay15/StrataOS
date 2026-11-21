package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"strataos/internal/photoml"
	"strataos/internal/photos"
	"strataos/internal/store"
)

// Smart photos: faces grouped into people (per user: each user names,
// merges, splits and hides the people of the pictures they can see),
// things and scenes, search by description, places from the GPS position
// (offline, GeoNames towns) and memories. The analysis runs in the
// background on the NAS (photoml worker, ONNX Runtime) once the "photoml"
// component is installed and enabled in Photos → Smart features.

type smartState struct {
	once   sync.Once
	db     *photoml.DB
	err    error
	worker *photoml.Worker
	kick   chan struct{}

	mu       sync.Mutex
	running  bool
	paused   string
	done     int
	total    int
	current  string
	lastErr  string
	labels   []photoml.Vec
	textMemo map[string]photoml.Vec
}

var smart smartState

func (s *Server) smartLimits() store.BackgroundLimits {
	var l store.BackgroundLimits
	s.store.Read(func(st *store.State) { l = st.Photos.Smart.Limits })
	return l
}

func (s *Server) smartDB() (*photoml.DB, error) {
	smart.once.Do(func() {
		smart.kick = make(chan struct{}, 1)
		smart.textMemo = map[string]photoml.Vec{}
		smart.worker = &photoml.Worker{Threads: func() int { return threadsOf(s.smartLimits()) }, Idle: 3 * time.Minute}
		smart.db, smart.err = photoml.OpenDB(filepath.Join(photos.Dir, "ml.db"))
	})
	return smart.db, smart.err
}

func kickSmart() {
	select {
	case smart.kick <- struct{}{}:
	default:
	}
}

func (s *Server) smartSettings() store.PhotoSmart {
	var p store.PhotoSmart
	s.store.Read(func(st *store.State) { p = st.Photos.Smart })
	return p
}

// smartLoop analyses new and changed pictures.
func (s *Server) smartLoop() {
	time.Sleep(2 * time.Minute)
	for {
		set := s.smartSettings()
		if set.Enabled && photoml.Installed() && !photos.Default.Updated.IsZero() {
			if err := s.smartPass(set); err != nil {
				slog.Warn("smart photos", "err", err)
				smart.mu.Lock()
				smart.lastErr = err.Error()
				smart.mu.Unlock()
			}
		}
		select {
		case <-smart.kick:
		case <-time.After(5 * time.Minute):
		}
	}
}

// labelVecs returns the embeddings of the tag prompts (computed once).
func (s *Server) labelVecs(db *photoml.DB) ([]photoml.Vec, error) {
	smart.mu.Lock()
	l := smart.labels
	smart.mu.Unlock()
	if l != nil {
		return l, nil
	}
	prompts := make([]string, len(photoml.Labels))
	for i, x := range photoml.Labels {
		prompts[i] = x.Prompt
	}
	vecs := db.LabelEmbeddings(prompts)
	var missing []string
	for i, v := range vecs {
		if v == nil {
			missing = append(missing, prompts[i])
		}
	}
	if len(missing) > 0 {
		got, err := smart.worker.Texts(missing)
		if err != nil {
			return nil, err
		}
		for i, p := range missing {
			_ = db.PutLabel(p, got[i])
		}
		vecs = db.LabelEmbeddings(prompts)
	}
	smart.mu.Lock()
	smart.labels = vecs
	smart.mu.Unlock()
	return vecs, nil
}

func (s *Server) smartPass(set store.PhotoSmart) error {
	db, err := s.smartDB()
	if err != nil {
		return err
	}
	want := 0
	if !set.NoFaces {
		want |= photoml.DoneFaces
	}
	if !set.NoObjects {
		want |= photoml.DoneClip
	}
	if want == 0 {
		return nil
	}
	items := photos.Default.List(func(string) bool { return true })
	done := db.Processed()
	inIndex := make(map[string]bool, len(items))
	var todo []photos.Item
	for _, it := range items {
		inIndex[it.Path] = true
		d, ok := done[it.Path]
		if !ok || d[0] != it.Mtime || int(d[1])&want != want {
			todo = append(todo, it)
		}
	}
	var gone []string
	for p := range done {
		if !inIndex[p] {
			gone = append(gone, p)
		}
	}
	if len(gone) > 0 {
		if err := db.Forget(gone); err != nil {
			return err
		}
	}
	smart.mu.Lock()
	smart.running, smart.done, smart.total, smart.lastErr = true, 0, len(todo), ""
	smart.mu.Unlock()
	defer func() {
		smart.mu.Lock()
		smart.running, smart.current, smart.paused = false, "", ""
		smart.mu.Unlock()
	}()
	var labels []photoml.Vec
	if want&photoml.DoneClip != 0 && len(todo) > 0 {
		if labels, err = s.labelVecs(db); err != nil {
			return err
		}
	}
	gate := s.backgroundGate(s.smartLimits, func() int { return threadsOf(s.smartLimits()) })
	ctx := context.Background()
	sinceGroup := 0
	for _, it := range todo {
		if cur := s.smartSettings(); !cur.Enabled || cur.NoFaces != set.NoFaces || cur.NoObjects != set.NoObjects {
			break // settings changed: the next pass takes them
		}
		if err := gate(ctx, func(r string) {
			smart.mu.Lock()
			smart.paused = r
			smart.mu.Unlock()
		}); err != nil {
			return err
		}
		smart.mu.Lock()
		smart.paused, smart.current = "", it.Path
		smart.mu.Unlock()
		if err := s.analyse(db, it, want, done[it.Path], labels); err != nil {
			return err
		}
		smart.mu.Lock()
		smart.done++
		smart.mu.Unlock()
		if sinceGroup++; sinceGroup >= 300 && want&photoml.DoneFaces != 0 {
			s.groupAll(db)
			sinceGroup = 0
		}
	}
	if want&photoml.DoneFaces != 0 {
		s.groupAll(db)
	}
	return nil
}

// analyse runs the worker on the large thumbnail of a picture (made by
// libvips or ffmpeg: any format, rotated, never the original in the worker).
// A picture that cannot be read is recorded with its error and not tried
// again until it changes; a failing worker stops the pass.
func (s *Server) analyse(db *photoml.DB, it photos.Item, want int, prev [2]int64, labels []photoml.Vec) error {
	steps := want
	if prev[0] == it.Mtime {
		steps &^= int(prev[1]) // only what is missing
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	thumb, err := photos.Thumb(ctx, it, "l")
	if err != nil {
		return db.PutPhoto(it.Path, it.Mtime, want, err.Error(), nil, nil, nil, true)
	}
	data, err := os.ReadFile(thumb)
	if err != nil {
		return err
	}
	res, err := smart.worker.Image(data, steps&photoml.DoneFaces != 0, steps&photoml.DoneClip != 0)
	if err != nil {
		if strings.Contains(err.Error(), "worker") || strings.Contains(err.Error(), "not installed") {
			return err
		}
		return db.PutPhoto(it.Path, it.Mtime, want, err.Error(), nil, nil, nil, true)
	}
	var clip photoml.Vec
	var tags []string
	if steps&photoml.DoneClip != 0 {
		if clip, err = photoml.DecodeVec(res.Clip, photoml.ClipDim); err != nil {
			return db.PutPhoto(it.Path, it.Mtime, want, "invalid answer of the worker", nil, nil, nil, true)
		}
		tags = photoml.Tags(clip, labels)
	}
	var faces []photoml.FaceResult
	for _, f := range res.Faces {
		v, err := photoml.DecodeVec(f.Emb, photoml.FaceDim)
		if err != nil {
			continue
		}
		faces = append(faces, photoml.FaceResult{Box: f.Box, Score: f.Score, Emb: v})
	}
	done := steps
	if prev[0] == it.Mtime {
		done |= int(prev[1])
	}
	return db.PutPhoto(it.Path, it.Mtime, done, "", clip, tags, faces, steps&photoml.DoneFaces != 0)
}

// groupAll groups the new faces of every user, over the pictures each can see.
func (s *Server) groupAll(db *photoml.DB) {
	type acct struct {
		name  string
		admin bool
	}
	var users []acct
	s.store.Read(func(st *store.State) {
		for _, u := range st.Users {
			if !u.Disabled {
				users = append(users, acct{u.Username, u.Role == "admin"})
			}
		}
	})
	for _, u := range users {
		roots := s.readableRootsFor(u.name, u.admin)
		if _, err := db.Group(u.name, func(p string) bool { return under(roots, p) }); err != nil {
			slog.Warn("smart photos: grouping", "user", u.name, "err", err)
		}
	}
}

// textVec is the CLIP embedding of a search (the last ones are kept).
func textVec(q string) (photoml.Vec, error) {
	smart.mu.Lock()
	v := smart.textMemo[q]
	smart.mu.Unlock()
	if v != nil {
		return v, nil
	}
	got, err := smart.worker.Texts([]string{"a photo of " + q})
	if err != nil {
		return nil, err
	}
	smart.mu.Lock()
	if len(smart.textMemo) > 200 {
		smart.textMemo = map[string]photoml.Vec{}
	}
	smart.textMemo[q] = got[0]
	smart.mu.Unlock()
	return got[0], nil
}

// visibleFaces returns the faces of the pictures the user can see.
func (s *Server) visibleFaces(r *http.Request, db *photoml.DB) ([]photoml.FaceRow, error) {
	roots := s.readableRoots(r)
	return db.Faces(userOf(r), func(p string) bool {
		_, ok := photos.Default.Get(p)
		return ok && under(roots, p)
	}, false)
}

type personView struct {
	photoml.PersonRow
	Count int              `json:"count"`
	Cover *photoml.FaceRow `json:"cover,omitempty"`
}

// faceCrop makes the square thumbnail of a face (from the large thumbnail).
func faceCrop(ctx context.Context, f photoml.FaceRow, it photos.Item) (string, error) {
	out := filepath.Join(photos.Dir, "thumbs", "faces", fmt.Sprintf("%d-%d.jpg", f.ID, it.Mtime))
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}
	thumb, err := photos.Thumb(ctx, it, "l")
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(thumb)
	if err != nil {
		return "", err
	}
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	bounds := img.Bounds()
	w, h := float64(bounds.Dx()), float64(bounds.Dy())
	// The face and a margin of 30 %, square.
	cx, cy := (f.Box[0]+f.Box[2]/2)*w, (f.Box[1]+f.Box[3]/2)*h
	side := max(f.Box[2]*w, f.Box[3]*h) * 1.6
	r := image.Rect(int(cx-side/2), int(cy-side/2), int(cx+side/2), int(cy+side/2)).Intersect(bounds)
	if r.Empty() {
		return "", errors.New("empty face")
	}
	const size = 160
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	sx, sy := float64(r.Dx())/size, float64(r.Dy())/size
	for y := range size {
		for x := range size {
			dst.Set(x, y, img.At(r.Min.X+int((float64(x)+0.5)*sx), r.Min.Y+int((float64(y)+0.5)*sy)))
		}
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 85}); err != nil {
		return "", err
	}
	return out, os.WriteFile(out, buf.Bytes(), 0o600)
}

func validPersonName(n string) (string, error) {
	n = strings.TrimSpace(n)
	if utf8.RuneCountInString(n) > 80 || strings.ContainsAny(n, "\n\r") {
		return "", badRequest("invalid name")
	}
	return n, nil
}

func (s *Server) smartRoutes() {
	// Status for everyone (whether the features are there), details for administrators.
	s.user("GET /api/photos/smart", func(r *http.Request) (any, error) {
		set := s.smartSettings()
		out := map[string]any{"installed": photoml.Installed(), "enabled": set.Enabled, "faces": set.Enabled && !set.NoFaces, "objects": set.Enabled && !set.NoObjects}
		smart.mu.Lock()
		out["running"], out["done"], out["total"], out["paused"] = smart.running, smart.done, smart.total, smart.paused
		if isAdmin(r) {
			out["current"], out["error"] = smart.current, smart.lastErr
		}
		smart.mu.Unlock()
		if isAdmin(r) {
			out["settings"] = set
			out["threads"] = threadsOf(set.Limits)
			out["modelBytes"] = photoml.ModelBytes()
			if db, err := s.smartDB(); err == nil {
				out["counts"] = db.Counts()
				out["dbBytes"] = db.Bytes()
				out["provider"] = smart.worker.Provider
			}
		}
		return out, nil
	})

	s.admin("PUT /api/photos/smart", func(r *http.Request) (any, error) {
		var req store.PhotoSmart
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := validLimits(req.Limits); err != nil {
			return nil, err
		}
		if req.Enabled && !photoml.Installed() {
			return nil, badRequest("install the smart photos component first")
		}
		if err := s.store.Update(func(st *store.State) error { st.Photos.Smart = req; return nil }); err != nil {
			return nil, err
		}
		if _, err := s.smartDB(); err != nil {
			return nil, err
		}
		if !req.Enabled {
			smart.worker.Stop()
		}
		s.audit(r, "photos", fmt.Sprintf("Smart photos: enabled=%v faces=%v objects=%v", req.Enabled, !req.NoFaces, !req.NoObjects))
		kickSmart()
		return nil, nil
	})

	// Analyse everything again (after a model update, for instance).
	s.admin("POST /api/photos/smart/reset", func(r *http.Request) (any, error) {
		db, err := s.smartDB()
		if err != nil {
			return nil, err
		}
		smart.mu.Lock()
		running := smart.running
		smart.mu.Unlock()
		if running {
			return nil, &Error{Status: http.StatusConflict, Msg: "the analysis is running: disable it first"}
		}
		if err := db.Clear(); err != nil {
			return nil, err
		}
		_ = os.RemoveAll(filepath.Join(photos.Dir, "thumbs", "faces"))
		s.audit(r, "photos", "Smart photos analysis reset")
		kickSmart()
		return nil, nil
	})

	s.user("GET /api/photos/people", func(r *http.Request) (any, error) {
		db, err := s.smartDB()
		if err != nil {
			return nil, err
		}
		faces, err := s.visibleFaces(r, db)
		if err != nil {
			return nil, err
		}
		people, err := db.People(userOf(r))
		if err != nil {
			return nil, err
		}
		views := map[int64]*personView{}
		for i, f := range faces {
			p, ok := people[f.Person]
			if !ok {
				continue
			}
			v := views[f.Person]
			if v == nil {
				v = &personView{PersonRow: p}
				views[f.Person] = v
			}
			v.Count++
			if v.Cover == nil || f.Score > v.Cover.Score {
				v.Cover = &faces[i]
			}
		}
		withHidden := r.URL.Query().Get("hidden") == "1"
		out := []*personView{}
		for _, v := range views {
			if !v.Hidden || withHidden {
				out = append(out, v)
			}
		}
		sort.Slice(out, func(i, j int) bool {
			if (out[i].Name != "") != (out[j].Name != "") {
				return out[i].Name != ""
			}
			if out[i].Count != out[j].Count {
				return out[i].Count > out[j].Count
			}
			return out[i].ID < out[j].ID
		})
		return out, nil
	})

	s.user("GET /api/photos/people/{id}/faces", func(r *http.Request) (any, error) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		db, err := s.smartDB()
		if err != nil {
			return nil, err
		}
		faces, err := s.visibleFaces(r, db)
		if err != nil {
			return nil, err
		}
		out := []photoml.FaceRow{}
		for _, f := range faces {
			if f.Person == id && id > 0 {
				out = append(out, f)
			}
		}
		return out, nil
	})

	s.user("PUT /api/photos/people/{id}", func(r *http.Request) (any, error) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		var req struct {
			Name   *string `json:"name"`
			Hidden *bool   `json:"hidden"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.Name != nil {
			n, err := validPersonName(*req.Name)
			if err != nil {
				return nil, err
			}
			req.Name = &n
		}
		db, err := s.smartDB()
		if err != nil {
			return nil, err
		}
		if err := db.UpdatePerson(userOf(r), id, req.Name, req.Hidden); errors.Is(err, photoml.ErrNoPerson) {
			return nil, notFound("person not found")
		} else if err != nil {
			return nil, err
		}
		return nil, nil
	})

	s.user("POST /api/photos/people/{id}/merge", func(r *http.Request) (any, error) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		var req struct {
			From []int64 `json:"from"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if len(req.From) == 0 || len(req.From) > 100 {
			return nil, badRequest("choose the people to merge")
		}
		db, err := s.smartDB()
		if err != nil {
			return nil, err
		}
		if err := db.Merge(userOf(r), id, req.From); errors.Is(err, photoml.ErrNoPerson) {
			return nil, notFound("person not found")
		} else if err != nil {
			return nil, err
		}
		return nil, nil
	})

	// Move faces: to another person ("to": id), a new one ("to": 0, "name"),
	// or nobody ("to": -1, "not this person").
	s.user("POST /api/photos/faces/move", func(r *http.Request) (any, error) {
		var req struct {
			Faces []int64 `json:"faces"`
			To    int64   `json:"to"`
			Name  string  `json:"name"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if len(req.Faces) == 0 || len(req.Faces) > 5000 {
			return nil, badRequest("choose faces")
		}
		name, err := validPersonName(req.Name)
		if err != nil {
			return nil, err
		}
		db, err := s.smartDB()
		if err != nil {
			return nil, err
		}
		// Only faces of pictures the user can see.
		faces, err := s.visibleFaces(r, db)
		if err != nil {
			return nil, err
		}
		visible := map[int64]bool{}
		for _, f := range faces {
			visible[f.ID] = true
		}
		for _, f := range req.Faces {
			if !visible[f] {
				return nil, notFound("face not found")
			}
		}
		id, err := db.MoveFaces(userOf(r), req.Faces, req.To, name)
		if errors.Is(err, photoml.ErrNoPerson) {
			return nil, notFound("person not found")
		}
		return map[string]int64{"person": id}, err
	})

	s.userRaw("GET /api/photos/face", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		db, err := s.smartDB()
		if err != nil {
			writeErr(w, err)
			return
		}
		f, err := db.Face(id)
		it, ok := photos.Default.Get(f.Path)
		if err != nil || !ok || !under(s.readableRoots(r), f.Path) {
			writeErr(w, notFound("face not found"))
			return
		}
		p, err := faceCrop(r.Context(), f, it)
		if err != nil {
			writeErr(w, &Error{Status: http.StatusUnprocessableEntity, Msg: err.Error()})
			return
		}
		file, err := os.Open(p)
		if err != nil {
			writeErr(w, notFound("face not found"))
			return
		}
		defer file.Close()
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "private, max-age=604800")
		http.ServeContent(w, r, "face.jpg", time.Unix(it.Mtime, 0), file)
	})

	s.user("GET /api/photos/tags", func(r *http.Request) (any, error) {
		db, err := s.smartDB()
		if err != nil {
			return nil, err
		}
		roots := s.readableRoots(r)
		type tagView struct {
			Tag   string `json:"tag"`
			Count int    `json:"count"`
			Cover string `json:"cover"`
			date  int64
		}
		by := map[string]*tagView{}
		err = db.Photos(func(p string, info photoml.PhotoInfo) {
			it, ok := photos.Default.Get(p)
			if !ok || !under(roots, p) {
				return
			}
			for _, t := range info.Tags {
				v := by[t]
				if v == nil {
					v = &tagView{Tag: t}
					by[t] = v
				}
				v.Count++
				if it.Date > v.date {
					v.date, v.Cover = it.Date, p
				}
			}
		})
		out := []*tagView{}
		for _, v := range by {
			out = append(out, v)
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Count != out[j].Count {
				return out[i].Count > out[j].Count
			}
			return out[i].Tag < out[j].Tag
		})
		return out, err
	})

	s.user("GET /api/photos/places", func(r *http.Request) (any, error) {
		roots := s.readableRoots(r)
		type placeView struct {
			photos.Place
			Count int    `json:"count"`
			Cover string `json:"cover"`
		}
		by := map[int]*placeView{}
		for _, it := range photos.Default.List(func(p string) bool { return under(roots, p) }) {
			if !it.HasGPS() {
				continue
			}
			pl, ok := photos.Nearest(it.Lat, it.Lon)
			if !ok {
				continue
			}
			v := by[pl.ID]
			if v == nil {
				v = &placeView{Place: pl, Cover: it.Path} // newest first
				by[pl.ID] = v
			}
			v.Count++
		}
		out := []*placeView{}
		for _, v := range by {
			out = append(out, v)
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Count != out[j].Count {
				return out[i].Count > out[j].Count
			}
			return out[i].ID < out[j].ID
		})
		return out, nil
	})

	s.user("GET /api/photos/memories", func(r *http.Request) (any, error) {
		roots := s.readableRoots(r)
		return photos.Memories(photos.Default.List(func(p string) bool { return under(roots, p) }), time.Now()), nil
	})

	// Search by description (CLIP), tags and file names; best first.
	s.user("GET /api/photos/search", func(r *http.Request) (any, error) {
		q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
		if q == "" || utf8.RuneCountInString(q) > 200 {
			return nil, badRequest("invalid search")
		}
		roots := s.readableRoots(r)
		list := photos.Default.List(func(p string) bool { return under(roots, p) })
		byPath := make(map[string]int, len(list))
		for i, it := range list {
			byPath[it.Path] = i
		}
		seen := map[string]bool{}
		out := []photos.Item{}
		addPath := func(p string) {
			if i, ok := byPath[p]; ok && !seen[p] {
				seen[p] = true
				out = append(out, list[i])
			}
		}
		set := s.smartSettings()
		smartOn := set.Enabled && !set.NoObjects && photoml.Installed()
		if smartOn {
			db, err := s.smartDB()
			if err != nil {
				return nil, err
			}
			var paths []string
			var vecs []photoml.Vec
			var tagged []string
			_ = db.Photos(func(p string, info photoml.PhotoInfo) {
				if _, ok := byPath[p]; !ok {
					return
				}
				paths = append(paths, p)
				vecs = append(vecs, info.Clip)
				if slices.Contains(info.Tags, q) {
					tagged = append(tagged, p)
				}
			})
			for _, p := range tagged {
				addPath(p)
			}
			qv, err := textVec(q)
			if err != nil {
				return nil, err
			}
			for _, sc := range photoml.Rank(qv, len(vecs), func(i int) photoml.Vec { return vecs[i] }) {
				addPath(paths[sc.Key])
			}
		}
		// File and folder names, relative to the storage.
		for _, it := range list {
			if len(out) >= 1000 {
				break
			}
			if strings.Contains(strings.ToLower(strings.TrimPrefix(it.Path, "/srv/strata")), q) {
				addPath(it.Path)
			}
		}
		return map[string]any{"items": out, "smart": smartOn}, nil
	})
}

// photoFilter narrows GET /api/photos to a person, a tag or a place.
func (s *Server) photoFilter(r *http.Request) (func(string) bool, error) {
	qv := r.URL.Query()
	switch {
	case qv.Get("person") != "":
		id, _ := strconv.ParseInt(qv.Get("person"), 10, 64)
		db, err := s.smartDB()
		if err != nil {
			return nil, err
		}
		faces, err := s.visibleFaces(r, db)
		if err != nil {
			return nil, err
		}
		in := map[string]bool{}
		for _, f := range faces {
			if f.Person == id && id > 0 {
				in[f.Path] = true
			}
		}
		return func(p string) bool { return in[p] }, nil
	case qv.Get("tag") != "":
		tag := qv.Get("tag")
		db, err := s.smartDB()
		if err != nil {
			return nil, err
		}
		in := map[string]bool{}
		err = db.Photos(func(p string, info photoml.PhotoInfo) {
			if slices.Contains(info.Tags, tag) {
				in[p] = true
			}
		})
		return func(p string) bool { return in[p] }, err
	case qv.Get("place") != "":
		id, err := strconv.Atoi(qv.Get("place"))
		if err != nil {
			return nil, badRequest("invalid place")
		}
		return func(p string) bool {
			it, ok := photos.Default.Get(p)
			if !ok || !it.HasGPS() {
				return false
			}
			pl, ok := photos.Nearest(it.Lat, it.Lon)
			return ok && pl.ID == id
		}, nil
	}
	return nil, nil
}
