// Package photos indexes the photo and video folders of the NAS (date taken
// from EXIF, else the file time) and makes thumbnails with libvips / ffmpeg.
package photos

import (
	"context"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"strataos/internal/sys"
)

var (
	Dir = "/var/lib/strata/photos" // index.json and thumbs/
)

var imageExt = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".heic": true, ".heif": true, ".webp": true, ".gif": true, ".tif": true, ".tiff": true, ".avif": true}
var videoExt = map[string]bool{".mp4": true, ".mov": true, ".m4v": true, ".mkv": true, ".webm": true, ".3gp": true, ".avi": true}

// Item is one photo or video.
type Item struct {
	Path  string `json:"path"`
	Date  int64  `json:"date"` // Unix time the picture was taken
	Mtime int64  `json:"mtime"`
	Size  int64  `json:"size"`
	Video bool   `json:"video,omitempty"`
	// Hash is the 64-bit perceptual hash of an image (dhash.go), used to
	// find similar photos; 0 when it was not computed (videos, tools missing).
	Hash uint64 `json:"hash,omitempty"`
	// HashFailed: when the hash last failed (Unix time); retried after hashRetry.
	HashFailed int64 `json:"hashFailed,omitempty"`
	// GPS position from the EXIF data (0, 0: none).
	Lat float64 `json:"lat,omitempty"`
	Lon float64 `json:"lon,omitempty"`
}

const (
	// hashRetry: a picture whose hash failed is tried again after this.
	hashRetry = 7 * 24 * time.Hour
	// hashBudget bounds the hashes caught up per scan (libraries indexed
	// before hashes existed): the rest follows at the next scans.
	hashBudget = 20 * time.Minute
)

// For the tests.
var (
	hashImage = dhash
	canHash   = func() bool { return sys.Has("vipsthumbnail") }
)

// needsHash: a picture without a hash, never tried or tried long ago.
func needsHash(it Item, now time.Time) bool {
	return !it.Video && it.Hash == 0 && (it.HashFailed == 0 || now.Sub(time.Unix(it.HashFailed, 0)) > hashRetry)
}

// HasGPS tells whether the picture has a position.
func (it Item) HasGPS() bool { return it.Lat != 0 || it.Lon != 0 }

// indexVersion: 2 added the GPS positions (read again once for old items).
const indexVersion = 2

// Installed reports whether thumbnails can be made.
func Installed() bool { return sys.Has("vipsthumbnail") && sys.Has("ffmpeg") }

type Index struct {
	mu      sync.RWMutex
	items   []Item
	byPath  map[string]int
	Updated time.Time
	running bool
	version int
}

var Default = &Index{byPath: map[string]int{}}

func indexFile() string { return filepath.Join(Dir, "index.json") }

// Load reads the saved index.
func (x *Index) Load() {
	b, err := os.ReadFile(indexFile())
	if err != nil {
		return
	}
	var saved struct {
		Updated time.Time `json:"updated"`
		Version int       `json:"version"`
		Items   []Item    `json:"items"`
	}
	if json.Unmarshal(b, &saved) == nil {
		x.mu.Lock()
		x.items, x.Updated, x.version = saved.Items, saved.Updated, saved.Version
		x.reindexLocked()
		x.mu.Unlock()
	}
}

func (x *Index) reindexLocked() {
	sort.Slice(x.items, func(i, j int) bool {
		if x.items[i].Date != x.items[j].Date {
			return x.items[i].Date > x.items[j].Date
		}
		return x.items[i].Path < x.items[j].Path
	})
	x.byPath = make(map[string]int, len(x.items))
	for i, it := range x.items {
		x.byPath[it.Path] = i
	}
}

// Get returns the item of a path.
func (x *Index) Get(p string) (Item, bool) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	i, ok := x.byPath[p]
	if !ok {
		return Item{}, false
	}
	return x.items[i], true
}

// List returns the items accepted by allow, newest first.
func (x *Index) List(allow func(string) bool) []Item {
	x.mu.RLock()
	defer x.mu.RUnlock()
	out := []Item{}
	for _, it := range x.items {
		if allow(it.Path) {
			out = append(out, it)
		}
	}
	return out
}

func (x *Index) Running() bool {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.running
}

func skipDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "@eaDir" || name == "#recycle" || name == "#snapshot"
}

// Scan walks the folders and updates the index (dates are read only for new
// or modified files). It returns the number of items.
func (x *Index) Scan(ctx context.Context, roots []string, logf func(string, ...any)) (int, error) {
	x.mu.Lock()
	if x.running {
		x.mu.Unlock()
		return 0, fmt.Errorf("indexing already running")
	}
	x.running = true
	version := x.version
	old := make(map[string]Item, len(x.items))
	for _, it := range x.items {
		old[it.Path] = it
	}
	x.mu.Unlock()
	defer func() {
		x.mu.Lock()
		x.running = false
		x.mu.Unlock()
	}()

	var items []Item
	added := 0
	hashable := canHash()
	now := time.Now()
	var missing []int // unchanged pictures still without a hash
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if p != root && skipDir(d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Type()&fs.ModeSymlink != 0 || !d.Type().IsRegular() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(p))
			video := videoExt[ext]
			if !imageExt[ext] && !video {
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				return nil
			}
			it := Item{Path: p, Mtime: fi.ModTime().Unix(), Size: fi.Size(), Video: video}
			if o, ok := old[p]; ok && o.Mtime == it.Mtime && o.Size == it.Size {
				if version >= indexVersion || video {
					if hashable && needsHash(o, now) {
						missing = append(missing, len(items))
					}
					items = append(items, o)
					return nil
				}
				// Read again for its position: the file did not change, its hash holds.
				it.Hash, it.HashFailed = o.Hash, o.HashFailed
			}
			it.Date, it.Lat, it.Lon = takenAt(p, ext, fi.ModTime())
			if hashable && needsHash(it, now) {
				if it.Hash = hashImage(ctx, p); it.Hash == 0 {
					it.HashFailed = now.Unix()
				}
			}
			items = append(items, it)
			added++
			if added%500 == 0 {
				logf("%d new photos indexed…", added)
			}
			return nil
		})
	}
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	// Pictures indexed without a hash (before 0.11, or a failure some days
	// ago): caught up within a budget per scan, so the Duplicates tab fills
	// in over the next scans without holding the index back.
	hashed := 0
	for _, i := range missing {
		if ctx.Err() != nil || time.Since(now) > hashBudget {
			break
		}
		if items[i].Hash = hashImage(ctx, items[i].Path); items[i].Hash == 0 {
			items[i].HashFailed = time.Now().Unix()
		} else {
			items[i].HashFailed = 0
			hashed++
		}
	}
	if hashed > 0 {
		logf("%d earlier photos hashed for the duplicates (%d waiting)", hashed, len(missing)-hashed)
	}
	x.mu.Lock()
	x.items, x.Updated, x.version = items, time.Now(), indexVersion
	x.reindexLocked()
	saved, _ := json.Marshal(map[string]any{"updated": x.Updated, "version": indexVersion, "items": x.items})
	x.mu.Unlock()
	if err := os.MkdirAll(Dir, 0o700); err == nil {
		_ = sys.WriteFileAtomic(indexFile(), saved, 0o600)
	}
	logf("%d photos and videos, %d new", len(items), added)
	return len(items), nil
}

// takenAt returns the EXIF date taken of a picture (else its modification
// time) and its GPS position.
func takenAt(p, ext string, mtime time.Time) (int64, float64, float64) {
	if ext == ".jpg" || ext == ".jpeg" {
		if m, ok := jpegMeta(p); ok && !m.date.IsZero() {
			return m.date.Unix(), m.lat, m.lon
		} else if ok {
			return mtime.Unix(), m.lat, m.lon
		}
	}
	date := mtime.Unix()
	var lat, lon float64
	if ext == ".heic" || ext == ".heif" || ext == ".tif" || ext == ".tiff" || ext == ".avif" || ext == ".webp" {
		if out, err := sys.Run("vipsheader", "-f", "exif-ifd2-DateTimeOriginal", p); err == nil {
			if t, ok := parseExifDate(strings.TrimSpace(out)); ok {
				date = t.Unix()
			}
		}
		// exiftool (Media tools) reads the positions of these formats.
		if sys.Has("exiftool") {
			if out, err := sys.Run("exiftool", "-n", "-s3", "-GPSLatitude", "-GPSLongitude", "--", p); err == nil {
				f := strings.Fields(out)
				if len(f) == 2 {
					la, e1 := strconv.ParseFloat(f[0], 64)
					lo, e2 := strconv.ParseFloat(f[1], 64)
					if e1 == nil && e2 == nil && validGPS(la, lo) {
						lat, lon = la, lo
					}
				}
			}
		}
	}
	return date, lat, lon
}

func validGPS(lat, lon float64) bool {
	return lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180 && (lat != 0 || lon != 0)
}

func parseExifDate(s string) (time.Time, bool) {
	if len(s) < 19 {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("2006:01:02 15:04:05", s[:19], time.Local)
	if err != nil || t.Year() < 1900 {
		return time.Time{}, false
	}
	return t, true
}

type exifMeta struct {
	date     time.Time
	lat, lon float64
}

// jpegMeta reads DateTimeOriginal (or DateTime) and the GPS position from
// the EXIF block of a JPEG.
func jpegMeta(p string) (exifMeta, bool) {
	f, err := os.Open(p)
	if err != nil {
		return exifMeta{}, false
	}
	defer f.Close()
	buf := make([]byte, 256<<10)
	n, _ := io.ReadFull(f, buf)
	return exifOf(buf[:n])
}

func exifDate(b []byte) (time.Time, bool) {
	m, ok := exifOf(b)
	return m.date, ok && !m.date.IsZero()
}

func exifOf(b []byte) (exifMeta, bool) {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return exifMeta{}, false
	}
	i := 2
	for i+4 <= len(b) {
		if b[i] != 0xFF {
			return exifMeta{}, false
		}
		marker := b[i+1]
		size := int(binary.BigEndian.Uint16(b[i+2:]))
		if marker == 0xDA || size < 2 { // start of scan: no more metadata
			return exifMeta{}, false
		}
		if marker == 0xE1 && i+4+size-2 <= len(b) && string(b[i+4:min(i+10, len(b))]) == "Exif\x00\x00" {
			return tiffMeta(b[i+10 : i+2+size])
		}
		i += 2 + size
	}
	return exifMeta{}, false
}

func tiffMeta(t []byte) (exifMeta, bool) {
	var m exifMeta
	if len(t) < 8 {
		return m, false
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return m, false
	}
	readIFD := func(off uint32) map[uint16][2]uint32 {
		tags := map[uint16][2]uint32{}
		if int(off)+2 > len(t) {
			return tags
		}
		n := int(bo.Uint16(t[off:]))
		for k := 0; k < n && k < 512; k++ {
			e := int(off) + 2 + k*12
			if e+12 > len(t) {
				break
			}
			tags[bo.Uint16(t[e:])] = [2]uint32{bo.Uint32(t[e+4:]), bo.Uint32(t[e+8:])} // count, value/offset
		}
		return tags
	}
	str := func(v [2]uint32) string {
		cnt, off := int(v[0]), int(v[1])
		if cnt <= 4 { // stored in the entry itself
			return ""
		}
		if cnt <= 0 || off+cnt > len(t) || cnt > 64 {
			return ""
		}
		return strings.TrimRight(string(t[off:off+cnt]), "\x00 ")
	}
	// Degrees, minutes, seconds: three rationals.
	dms := func(v [2]uint32) (float64, bool) {
		off := int(v[1])
		if v[0] != 3 || off+24 > len(t) {
			return 0, false
		}
		var out float64
		for k, div := range []float64{1, 60, 3600} {
			num, den := bo.Uint32(t[off+8*k:]), bo.Uint32(t[off+8*k+4:])
			if den == 0 {
				return 0, false
			}
			out += float64(num) / float64(den) / div
		}
		return out, true
	}
	ifd0 := readIFD(bo.Uint32(t[4:]))
	if ptr, ok := ifd0[0x8769]; ok {
		exif := readIFD(ptr[1])
		for _, tag := range []uint16{0x9003, 0x9004} { // DateTimeOriginal, DateTimeDigitized
			if v, ok := exif[tag]; ok {
				if d, ok := parseExifDate(str(v)); ok {
					m.date = d
					break
				}
			}
		}
	}
	if m.date.IsZero() {
		if v, ok := ifd0[0x0132]; ok { // DateTime
			m.date, _ = parseExifDate(str(v))
		}
	}
	if ptr, ok := ifd0[0x8825]; ok { // GPS IFD
		g := readIFD(ptr[1])
		lat, ok1 := dms(g[2])
		lon, ok2 := dms(g[4])
		// The references (N/S, E/W) are one character stored in the entry.
		if ok1 && ok2 {
			if ref, ok := g[1]; ok && refChar(bo, ref[1]) == 'S' {
				lat = -lat
			}
			if ref, ok := g[3]; ok && refChar(bo, ref[1]) == 'W' {
				lon = -lon
			}
			if validGPS(lat, lon) {
				m.lat, m.lon = lat, lon
			}
		}
	}
	return m, true
}

// refChar is the first byte of a value stored inside an IFD entry.
func refChar(bo binary.ByteOrder, v uint32) byte {
	var b [4]byte
	bo.PutUint32(b[:], v)
	return b[0]
}

// ---- Thumbnails ----

var thumbSem = make(chan struct{}, 2)

// Sizes: small for the grid, large for the viewer (formats browsers cannot show, e.g. HEIC).
var sizes = map[string]int{"s": 360, "l": 1600}

// Thumb returns the path of the cached thumbnail of it, making it if needed.
func Thumb(ctx context.Context, it Item, size string) (string, error) {
	px, ok := sizes[size]
	if !ok {
		return "", fmt.Errorf("invalid size")
	}
	h := sha1.Sum([]byte(it.Path + "\x00" + strconv.FormatInt(it.Mtime, 10) + "\x00" + size))
	name := hex.EncodeToString(h[:])
	out := filepath.Join(Dir, "thumbs", name[:2], name+".jpg")
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}
	if !Installed() {
		return "", fmt.Errorf("thumbnails need libvips and ffmpeg")
	}
	select {
	case thumbSem <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-thumbSem }()
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		return "", err
	}
	tmp := out + ".tmp.jpg"
	var err error
	if it.Video {
		_, err = sys.RunOpts(ctx, sys.Opts{Timeout: time.Minute}, "ffmpeg", "-nostdin", "-loglevel", "error", "-y", "-ss", "1", "-i", it.Path, "-frames:v", "1", "-vf", fmt.Sprintf("scale='min(%d,iw)':-2", px), tmp)
		if err != nil { // shorter than one second
			_, err = sys.RunOpts(ctx, sys.Opts{Timeout: time.Minute}, "ffmpeg", "-nostdin", "-loglevel", "error", "-y", "-i", it.Path, "-frames:v", "1", "-vf", fmt.Sprintf("scale='min(%d,iw)':-2", px), tmp)
		}
	} else {
		// vipsthumbnail applies the EXIF orientation and never upscales.
		_, err = sys.RunOpts(ctx, sys.Opts{Timeout: time.Minute}, "vipsthumbnail", it.Path, "--size", fmt.Sprintf("%dx%d>", px, px), "-o", tmp+"[Q=82,strip]")
	}
	if err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("thumbnail: %w", err)
	}
	return out, os.Rename(tmp, out)
}
