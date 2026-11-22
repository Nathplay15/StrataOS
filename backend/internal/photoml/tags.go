package photoml

import (
	"math"
	"sort"
)

// Label is a thing or scene recognised in pictures: its tag (English, the
// interface translates it: frontend/src/lib/photoTags.ts lists the same
// tags) and the sentence CLIP compares pictures with.
type Label struct {
	Tag    string
	Prompt string
}

func photoOf(tags ...string) []Label {
	out := make([]Label, len(tags))
	for i, t := range tags {
		out[i] = Label{t, "a photo of a " + t + "."}
	}
	return out
}

func photoThe(tags ...string) []Label {
	out := make([]Label, len(tags))
	for i, t := range tags {
		out[i] = Label{t, "a photo of the " + t + "."}
	}
	return out
}

// Labels are the tags given to pictures (zero-shot CLIP classification).
var Labels = concat(
	photoOf("dog", "cat", "bird", "horse", "cow", "sheep", "fish", "insect", "butterfly",
		"car", "bicycle", "motorcycle", "bus", "truck", "train", "airplane", "boat",
		"house", "church", "castle", "bridge", "tower", "skyscraper", "street", "road",
		"forest", "lake", "river", "waterfall", "mountain", "desert", "field", "garden", "park", "island",
		"flower", "tree", "sunset", "rainbow", "fireworks", "christmas tree", "swimming pool", "playground",
		"cake", "pizza", "salad", "dessert", "fruit", "drink", "restaurant", "kitchen",
		"baby", "child", "group of people", "wedding", "birthday party", "concert", "stadium", "museum",
		"computer", "phone", "book", "toy", "painting", "statue", "guitar", "piano",
		"tent", "campfire", "ski slope", "football match", "tennis court", "boat on the sea"),
	photoThe("beach", "sea", "snow", "night sky", "city", "countryside", "sky", "moon"),
	[]Label{
		{"selfie", "a selfie."},
		{"portrait", "a portrait photo of a person."},
		{"document", "a photo of a paper document with text."},
		{"screenshot", "a screenshot of a computer screen."},
		{"receipt", "a photo of a receipt."},
		{"food", "a photo of a meal on a plate."},
		{"car interior", "a photo taken inside a car."},
		{"indoor", "a photo of a room indoors."},
	},
)

func concat(lists ...[]Label) []Label {
	var out []Label
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

// Tag selection: CLIP's logits are 100 × the cosine similarity; a label
// whose softmax probability over all labels reaches MinTagProb (and its
// similarity MinTagSim) tags the picture, three tags at most.
const (
	MinTagProb = 0.2
	MinTagSim  = 0.2
	MaxTags    = 3
)

// Tags returns the tags of a picture embedding; labels are the embeddings
// of Labels, in the same order.
func Tags(img Vec, labels []Vec) []string {
	if len(labels) == 0 {
		return nil
	}
	sims := make([]float64, len(labels))
	top := math.Inf(-1)
	for i, l := range labels {
		sims[i] = float64(Dot(img, l))
		top = max(top, sims[i])
	}
	var sum float64
	probs := make([]float64, len(labels))
	for i, s := range sims {
		probs[i] = math.Exp(100 * (s - top))
		sum += probs[i]
	}
	idx := make([]int, len(labels))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return sims[idx[a]] > sims[idx[b]] })
	var out []string
	for _, i := range idx[:min(MaxTags, len(idx))] {
		if probs[i]/sum >= MinTagProb && sims[i] >= MinTagSim && i < len(Labels) {
			out = append(out, Labels[i].Tag)
		}
	}
	return out
}

// IsTag tells whether t is one of the tags.
func IsTag(t string) bool {
	for _, l := range Labels {
		if l.Tag == t {
			return true
		}
	}
	return false
}
