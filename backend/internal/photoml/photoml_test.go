package photoml

import (
	"math"
	"math/rand"
	"path/filepath"
	"strings"
	"testing"
)

// around returns a unit vector close to center (noise of the given size).
func around(r *rand.Rand, center []float32, noise float64) Vec {
	f := make([]float32, len(center))
	for i, c := range center {
		f[i] = c + float32(r.NormFloat64()*noise)
	}
	return Quantize(f)
}

func randomDir(r *rand.Rand, dim int) []float32 {
	f := make([]float32, dim)
	for i := range f {
		f[i] = float32(r.NormFloat64())
	}
	return f
}

func TestQuantizeDot(t *testing.T) {
	a := Quantize([]float32{3, 4})
	if d := Dot(a, a); math.Abs(float64(d)-1) > 0.02 {
		t.Errorf("self similarity %v", d)
	}
	b := Quantize([]float32{-4, 3})
	if d := Dot(a, b); math.Abs(float64(d)) > 0.02 {
		t.Errorf("orthogonal similarity %v", d)
	}
	v, err := DecodeVec("AQL/", 3)
	if err != nil || v[0] != 1 || v[1] != 2 || v[2] != -1 {
		t.Errorf("decode: %v %v", v, err)
	}
	if _, err := DecodeVec("AQL/", 4); err == nil {
		t.Error("wrong size accepted")
	}
	if Dot(a, Vec{1}) != -1 {
		t.Error("different sizes")
	}
}

func TestClusterPeople(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	alice, bob, carol := randomDir(r, FaceDim), randomDir(r, FaceDim), randomDir(r, FaceDim)
	var fresh []Face
	id := int64(0)
	add := func(c []float32, n int) []int64 {
		var ids []int64
		for range n {
			id++
			fresh = append(fresh, Face{id, around(r, c, 0.05)})
			ids = append(ids, id)
		}
		return ids
	}
	aliceIDs := add(alice, 5)
	bobIDs := add(bob, 3)
	carolIDs := add(carol, 1) // a stranger seen once
	r.Shuffle(len(fresh), func(i, j int) { fresh[i], fresh[j] = fresh[j], fresh[i] })

	g := Cluster(nil, nil, fresh)
	if len(g.New) != 2 || len(g.Assign) != 0 || len(g.Alone) != 1 || g.Alone[0] != carolIDs[0] {
		t.Fatalf("first run: %+v", g)
	}
	sets := map[string]bool{}
	for _, grp := range g.New {
		ids := map[int64]bool{}
		for _, f := range grp {
			ids[f] = true
		}
		switch {
		case len(grp) == 5 && ids[aliceIDs[0]] && ids[aliceIDs[4]]:
			sets["alice"] = true
		case len(grp) == 3 && ids[bobIDs[0]] && ids[bobIDs[2]]:
			sets["bob"] = true
		default:
			t.Errorf("mixed group %v", grp)
		}
	}
	if !sets["alice"] || !sets["bob"] {
		t.Fatalf("groups: %v", g.New)
	}

	// Next run: Alice is person 10, Bob 11, Carol in the pool; new faces of
	// Alice join her, a second Carol makes a new person with the first.
	var aliceVecs, bobVecs []Vec
	for _, f := range fresh {
		for _, a := range aliceIDs {
			if f.ID == a {
				aliceVecs = append(aliceVecs, f.Emb)
			}
		}
		for _, b := range bobIDs {
			if f.ID == b {
				bobVecs = append(bobVecs, f.Emb)
			}
		}
	}
	people := []Person{PersonOf(10, aliceVecs), PersonOf(11, bobVecs)}
	pool := []Face{{carolIDs[0], around(r, carol, 0.05)}}
	fresh2 := []Face{{100, around(r, alice, 0.05)}, {101, around(r, carol, 0.05)}, {102, Quantize(randomDir(r, FaceDim))}}
	g = Cluster(people, pool, fresh2)
	if g.Assign[100] != 10 || len(g.Assign) != 1 {
		t.Errorf("assign: %v", g.Assign)
	}
	if len(g.New) != 1 || len(g.New[0]) != 2 {
		t.Errorf("new: %v", g.New)
	}
	if len(g.Alone) != 1 || g.Alone[0] != 102 {
		t.Errorf("alone: %v", g.Alone)
	}
}

func TestRank(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	q := randomDir(r, ClipDim)
	qv := Quantize(q)
	// Build vectors with a chosen similarity s to q: s·q + sqrt(1-s²)·noise⊥.
	mk := func(s float64) Vec {
		n := randomDir(r, ClipDim)
		var dot, qq float64
		for i := range n {
			dot += float64(n[i] * q[i])
			qq += float64(q[i] * q[i])
		}
		for i := range n {
			n[i] -= float32(dot / qq * float64(q[i]))
		}
		var nn float64
		for _, x := range n {
			nn += float64(x * x)
		}
		f := make([]float32, ClipDim)
		for i := range f {
			f[i] = float32(s*float64(q[i])/math.Sqrt(qq) + math.Sqrt(1-s*s)*float64(n[i])/math.Sqrt(nn))
		}
		return Quantize(f)
	}
	vecs := []Vec{mk(0.20), mk(0.30), mk(0.27), nil, mk(0.24), mk(0.10)}
	got := Rank(qv, len(vecs), func(i int) Vec { return vecs[i] })
	var keys []int
	for _, s := range got {
		keys = append(keys, s.Key)
	}
	// 0.30 and 0.27 kept, 0.24 is beyond the window of the best (0.06), the others too low.
	if len(keys) != 2 || keys[0] != 1 || keys[1] != 2 {
		t.Errorf("rank: %v %+v", keys, got)
	}
	if got := Rank(qv, 1, func(int) Vec { return mk(0.1) }); len(got) != 0 {
		t.Errorf("weak matches kept: %v", got)
	}
}

func TestTags(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	labels := make([]Vec, len(Labels))
	dirs := make([][]float32, len(Labels))
	for i := range labels {
		dirs[i] = randomDir(r, ClipDim)
		labels[i] = Quantize(dirs[i])
	}
	dog := -1
	for i, l := range Labels {
		if l.Tag == "dog" {
			dog = i
		}
	}
	// A picture close to "dog": one tag.
	img := make([]float32, ClipDim)
	for i := range img {
		img[i] = dirs[dog][i]*0.3 + float32(r.NormFloat64())
	}
	tags := Tags(Quantize(img), labels)
	if len(tags) != 1 || tags[0] != "dog" {
		t.Errorf("tags: %v", tags)
	}
	// Unrelated to every label: no tag.
	if tags := Tags(Quantize(randomDir(r, ClipDim)), labels); len(tags) != 0 {
		t.Errorf("random picture tagged %v", tags)
	}
	if !IsTag("beach") || IsTag("unicorn") {
		t.Error("IsTag")
	}
	seen := map[string]bool{}
	for _, l := range Labels {
		if seen[l.Tag] || strings.Contains(l.Tag, ",") {
			t.Errorf("tag %q repeated or with a comma", l.Tag)
		}
		seen[l.Tag] = true
	}
}

func TestDBPeoplePerUser(t *testing.T) {
	d, err := OpenDB(filepath.Join(t.TempDir(), "ml.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	r := rand.New(rand.NewSource(4))
	alice := randomDir(r, FaceDim)
	// Three pictures of Alice: two in Team, one in Private.
	for _, p := range []string{"/srv/Team/a.jpg", "/srv/Team/b.jpg", "/srv/Private/c.jpg"} {
		if err := d.PutPhoto(p, 1, DoneFaces, "", nil, nil, []FaceResult{{Box: [4]float64{0.1, 0.1, 0.2, 0.2}, Score: 0.9, Emb: around(r, alice, 0.05)}}, true); err != nil {
			t.Fatal(err)
		}
	}
	team := func(p string) bool { return strings.HasPrefix(p, "/srv/Team/") }
	all := func(string) bool { return true }

	// Bob sees Team only: one person of two faces; the admin sees three.
	if n, err := d.Group("bob", team); err != nil || n != 1 {
		t.Fatalf("bob: %d %v", n, err)
	}
	if n, _ := d.Group("admin", all); n != 1 {
		t.Fatal("admin grouping")
	}
	bobFaces, _ := d.Faces("bob", team, false)
	if len(bobFaces) != 2 || bobFaces[0].Person <= 0 || bobFaces[0].Person != bobFaces[1].Person {
		t.Fatalf("bob faces: %+v", bobFaces)
	}
	bobAlice := bobFaces[0].Person
	// Names are per user.
	name := "Alice"
	if err := d.UpdatePerson("bob", bobAlice, &name, nil); err != nil {
		t.Fatal(err)
	}
	if err := d.UpdatePerson("admin", bobAlice, &name, nil); err != ErrNoPerson {
		t.Errorf("someone else's person renamed: %v", err)
	}
	adminPeople, _ := d.People("admin")
	for _, p := range adminPeople {
		if p.Name != "" {
			t.Error("bob's name leaked to the admin")
		}
	}
	// Split one face off ("not Alice"), then grouping leaves it alone.
	if _, err := d.MoveFaces("bob", []int64{bobFaces[1].ID}, -1, ""); err != nil {
		t.Fatal(err)
	}
	d.PutPhoto("/srv/Team/d.jpg", 1, DoneFaces, "", nil, nil, []FaceResult{{Emb: around(r, alice, 0.05)}}, true)
	d.Group("bob", team)
	bobFaces, _ = d.Faces("bob", team, false)
	for _, f := range bobFaces {
		switch f.ID {
		case bobFaces[0].ID:
		case 2:
			if f.Person != 0 || !f.Manual {
				t.Errorf("moved face regrouped: %+v", f)
			}
		default:
			if f.Person != bobAlice {
				t.Errorf("new face not given to Alice: %+v", f)
			}
		}
	}
	// A new person from a face, then merged back into Alice.
	p2, err := d.MoveFaces("bob", []int64{2}, 0, "Twin")
	if err != nil || p2 == 0 {
		t.Fatal(err)
	}
	if err := d.Merge("bob", bobAlice, []int64{p2}); err != nil {
		t.Fatal(err)
	}
	people, _ := d.People("bob")
	if len(people) != 1 || people[bobAlice].Name != "Alice" {
		t.Errorf("after merge: %+v", people)
	}
	// A picture analysed again loses its old faces; forgetting removes all.
	d.PutPhoto("/srv/Team/a.jpg", 2, DoneFaces, "", nil, nil, nil, true)
	d.Forget([]string{"/srv/Team/b.jpg"})
	if c := d.Counts(); c.Faces != 2 || c.Photos != 3 {
		t.Errorf("counts: %+v", c)
	}
	if err := d.ForgetOwner("bob"); err != nil {
		t.Fatal(err)
	}
	if people, _ := d.People("bob"); len(people) != 0 {
		t.Error("people of a deleted user kept")
	}
}

func TestDBClipAndLabels(t *testing.T) {
	d, err := OpenDB(filepath.Join(t.TempDir(), "ml.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	v := Quantize([]float32{1, 2, 3})
	d.PutPhoto("/p/a.jpg", 5, DoneClip, "", v, []string{"dog", "beach"}, nil, false)
	// Faces only: the CLIP result stays.
	d.PutPhoto("/p/a.jpg", 5, DoneClip|DoneFaces, "", nil, nil, nil, true)
	n := 0
	d.Photos(func(p string, info PhotoInfo) {
		n++
		if p != "/p/a.jpg" || len(info.Tags) != 2 || Dot(info.Clip, v) < 0.99 {
			t.Errorf("photo: %s %+v", p, info)
		}
	})
	if n != 1 {
		t.Error("photos")
	}
	if got := d.Processed()["/p/a.jpg"]; got != [2]int64{5, DoneClip | DoneFaces} {
		t.Errorf("processed: %v", got)
	}
	d.PutLabel("a photo of a dog.", v) // wrong size: ignored
	big := Quantize(make([]float32, ClipDim))
	d.PutLabel("a photo of a cat.", big)
	got := d.LabelEmbeddings([]string{"a photo of a dog.", "a photo of a cat.", "x"})
	if got[0] != nil || len(got[1]) != ClipDim || got[2] != nil {
		t.Errorf("labels: %v", got)
	}
}
