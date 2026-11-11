package photos

import (
	"sort"
	"time"
)

// Memory is the pictures taken on this day some years ago.
type Memory struct {
	Year     int    `json:"year"`
	YearsAgo int    `json:"yearsAgo"`
	Count    int    `json:"count"`
	Items    []Item `json:"items"` // the first 60
}

// Memories groups by year the items (newest first, as the index keeps
// them) taken on the day and month of now in earlier years, in local time.
func Memories(items []Item, now time.Time) []Memory {
	byYear := map[int]*Memory{}
	for _, it := range items {
		t := time.Unix(it.Date, 0).In(now.Location())
		if t.Year() >= now.Year() || t.Month() != now.Month() || t.Day() != now.Day() {
			continue
		}
		m := byYear[t.Year()]
		if m == nil {
			m = &Memory{Year: t.Year(), YearsAgo: now.Year() - t.Year(), Items: []Item{}}
			byYear[t.Year()] = m
		}
		m.Count++
		if len(m.Items) < 60 {
			m.Items = append(m.Items, it)
		}
	}
	out := []Memory{}
	for _, m := range byYear {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Year > out[j].Year })
	return out
}
