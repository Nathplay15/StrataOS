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

// Recap is one past month summarized for the gallery.
type Recap struct {
	Year  int    `json:"year"`
	Month int    `json:"month"` // 1-12
	Count int    `json:"count"`
	Items []Item `json:"items"` // a spread selection of the month, up to perMonth
}

// Recaps groups the items by month (local time), newest month first, the
// current month excluded, and picks an evenly spread selection of each month
// (items are newest first, as the index keeps them).
func Recaps(items []Item, now time.Time, perMonth int) []Recap {
	if perMonth <= 0 {
		perMonth = 24
	}
	byMonth := map[[2]int][]Item{}
	var order [][2]int
	for _, it := range items {
		t := time.Unix(it.Date, 0).In(now.Location())
		if t.Year() == now.Year() && t.Month() == now.Month() {
			continue
		}
		k := [2]int{t.Year(), int(t.Month())}
		if _, ok := byMonth[k]; !ok {
			order = append(order, k)
		}
		byMonth[k] = append(byMonth[k], it)
	}
	out := []Recap{}
	for _, k := range order {
		its := byMonth[k]
		out = append(out, Recap{Year: k[0], Month: k[1], Count: len(its), Items: spread(its, perMonth)})
	}
	return out
}

// spread picks up to n items evenly spaced in its (newest first).
func spread(its []Item, n int) []Item {
	if len(its) <= n {
		return its
	}
	out := make([]Item, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, its[i*len(its)/n])
	}
	return out
}
