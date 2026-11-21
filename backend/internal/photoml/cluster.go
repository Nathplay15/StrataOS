// Package photoml adds the smart features of Photos, computed on the NAS
// itself: faces grouped into people, things and scenes recognised in the
// pictures, and search by description ("dog on a beach"). A Python worker
// (worker.py, ONNX Runtime) turns pictures into embeddings; this package
// stores them (SQLite), groups faces, tags and ranks pictures.
package photoml

import (
	"encoding/base64"
	"errors"
	"math"
	"sort"
)

// Vec is an L2-normalized embedding quantized to int8 (x 127).
type Vec []int8

// DecodeVec reads the base64 form the worker answers.
func DecodeVec(s string, dim int) (Vec, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(b) != dim {
		return nil, errors.New("embedding of the wrong size")
	}
	v := make(Vec, dim)
	for i, c := range b {
		v[i] = int8(c)
	}
	return v, nil
}

// Bytes is the stored form.
func (v Vec) Bytes() []byte {
	b := make([]byte, len(v))
	for i, c := range v {
		b[i] = byte(c)
	}
	return b
}

// VecOf reads the stored form.
func VecOf(b []byte) Vec {
	v := make(Vec, len(b))
	for i, c := range b {
		v[i] = int8(c)
	}
	return v
}

// Dot is the cosine similarity of two quantized unit vectors.
func Dot(a, b Vec) float32 {
	if len(a) != len(b) {
		return -1
	}
	var s int32
	for i := range a {
		s += int32(a[i]) * int32(b[i])
	}
	return float32(s) / (127 * 127)
}

// Quantize normalizes f and quantizes it.
func Quantize(f []float32) Vec {
	var n float64
	for _, x := range f {
		n += float64(x) * float64(x)
	}
	n = math.Sqrt(n)
	v := make(Vec, len(f))
	if n == 0 {
		return v
	}
	for i, x := range f {
		q := math.Round(float64(x) / n * 127)
		v[i] = int8(max(-127, min(127, q)))
	}
	return v
}

// ---- Faces into people ----

// FaceThreshold is the cosine similarity of an SFace embedding to the mean
// of a person above which the face is that person. SFace's own threshold for
// two single faces is 0.363; against a mean of several faces a slightly
// higher value keeps look-alikes apart.
const FaceThreshold = 0.40

// MinFaces is the number of similar faces that makes a new person: one
// stranger in the background of a picture is not a person to name.
const MinFaces = 2

// Face is a detected face of a picture.
type Face struct {
	ID  int64
	Emb Vec
}

// Person is a group of faces with its sum of embeddings (its mean direction).
type Person struct {
	ID  int64 // 0 for a group made by this run
	Sum []float32
	N   int
}

func (p *Person) add(v Vec) {
	if p.Sum == nil {
		p.Sum = make([]float32, len(v))
	}
	for i, x := range v {
		p.Sum[i] += float32(x) / 127
	}
	p.N++
}

// PersonOf builds a person from its faces.
func PersonOf(id int64, faces []Vec) Person {
	p := Person{ID: id}
	for _, f := range faces {
		p.add(f)
	}
	return p
}

// similarity of v to the mean direction of p.
func (p *Person) similarity(v Vec) float32 {
	var dot, norm float64
	for i, x := range p.Sum {
		dot += float64(x) * float64(v[i]) / 127
		norm += float64(x) * float64(x)
	}
	if norm == 0 {
		return -1
	}
	return float32(dot / math.Sqrt(norm))
}

// Grouping is the result of Cluster: faces added to existing people, new
// people (each a list of faces) and the faces left alone.
type Grouping struct {
	Assign map[int64]int64 // face → existing person
	New    [][]int64       // faces of each new person
	Alone  []int64         // faces of no one yet
}

// Cluster groups fresh faces: each joins the closest person (existing, or
// formed by this run with faces of the pool or other fresh faces) when it
// is similar enough, otherwise it starts a group of its own. Groups that
// reach MinFaces become new people; the faces of smaller groups stay alone
// (the pool of the next run). Faces are taken in the order given (stable
// results for the same input).
func Cluster(people []Person, pool, fresh []Face) Grouping {
	type group struct {
		p      Person
		faces  []int64
		exists bool
	}
	groups := make([]*group, 0, len(people)+len(pool)+len(fresh))
	for _, p := range people {
		if p.N > 0 {
			groups = append(groups, &group{p: p, exists: true})
		}
	}
	for _, f := range pool {
		g := &group{}
		g.p.add(f.Emb)
		g.faces = []int64{f.ID}
		groups = append(groups, g)
	}
	for _, f := range fresh {
		best, bestSim := -1, float32(FaceThreshold)
		for i, g := range groups {
			if s := g.p.similarity(f.Emb); s >= bestSim {
				best, bestSim = i, s
			}
		}
		if best < 0 {
			g := &group{}
			g.p.add(f.Emb)
			g.faces = []int64{f.ID}
			groups = append(groups, g)
			continue
		}
		groups[best].p.add(f.Emb)
		groups[best].faces = append(groups[best].faces, f.ID)
	}
	out := Grouping{Assign: map[int64]int64{}}
	for _, g := range groups {
		switch {
		case g.exists:
			for _, f := range g.faces {
				out.Assign[f] = g.p.ID
			}
		case len(g.faces) >= MinFaces:
			out.New = append(out.New, g.faces)
		default:
			out.Alone = append(out.Alone, g.faces...)
		}
	}
	sort.Slice(out.Alone, func(i, j int) bool { return out.Alone[i] < out.Alone[j] })
	return out
}

// ---- Search ----

// Scored is a picture and its similarity to a search.
type Scored struct {
	Key   int
	Score float32
}

// Search thresholds of CLIP ViT-B/32 cosine similarities: a picture that
// matches a description scores around 0.25–0.33, an unrelated one under
// 0.22. Results must reach MinScore and stay close to the best one.
const (
	MinScore    = 0.235
	ScoreWindow = 0.06
)

// Rank keeps the pictures matching q, best first. emb returns the picture
// embeddings by key (nil to skip one).
func Rank(q Vec, n int, emb func(i int) Vec) []Scored {
	var out []Scored
	var best float32
	for i := range n {
		v := emb(i)
		if v == nil {
			continue
		}
		s := Dot(q, v)
		if s >= MinScore {
			out = append(out, Scored{i, s})
			best = max(best, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	cut := len(out)
	for i, s := range out {
		if s.Score < best-ScoreWindow {
			cut = i
			break
		}
	}
	return out[:cut]
}
