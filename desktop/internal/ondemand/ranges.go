package ondemand

import "sort"

// Range is the bytes [Off, End) of a file.
type Range struct {
	Off int64 `json:"o"`
	End int64 `json:"e"`
}

// Len is the number of bytes of the range.
func (r Range) Len() int64 { return r.End - r.Off }

// Ranges is a set of byte ranges of a file (the parts downloaded), sorted,
// without overlap nor contact: adjacent ranges are merged.
type Ranges []Range

// Add marks [off, end) as present.
func (s Ranges) Add(off, end int64) Ranges {
	if end <= off {
		return s
	}
	out := make(Ranges, 0, len(s)+1)
	n := Range{off, end}
	placed := false
	for _, r := range s {
		switch {
		case r.End < n.Off:
			out = append(out, r)
		case n.End < r.Off:
			if !placed {
				out = append(out, n)
				placed = true
			}
			out = append(out, r)
		default: // overlap or contact: merge into n
			n.Off, n.End = min(n.Off, r.Off), max(n.End, r.End)
		}
	}
	if !placed {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Off < out[j].Off })
	return out
}

// Truncate keeps what lies below size.
func (s Ranges) Truncate(size int64) Ranges {
	var out Ranges
	for _, r := range s {
		if r.Off >= size {
			break
		}
		out = append(out, Range{r.Off, min(r.End, size)})
	}
	return out
}

// Has tells whether [off, end) is entirely present.
func (s Ranges) Has(off, end int64) bool {
	if end <= off {
		return true
	}
	for _, r := range s {
		if r.Off <= off && end <= r.End {
			return true
		}
	}
	return false
}

// Total is the number of bytes present.
func (s Ranges) Total() int64 {
	var t int64
	for _, r := range s {
		t += r.Len()
	}
	return t
}

// Complete tells whether the whole file of that size is present.
func (s Ranges) Complete(size int64) bool { return size == 0 || s.Has(0, size) }

// Missing returns the parts of [off, end) not present, each widened to
// whole blocks of align bytes (clipped to size) so that small reads fetch
// sensible amounts and the Windows hydration rules (4 KiB multiples except
// at the end of the file) hold. Parts closer than gap bytes are fetched as
// one request.
func (s Ranges) Missing(off, end, size, align, gap int64) Ranges {
	end = min(end, size)
	if off < 0 {
		off = 0
	}
	if end <= off {
		return nil
	}
	if align > 1 {
		off = off / align * align
		end = min((end+align-1)/align*align, size)
	}
	var out Ranges
	cur := off
	for _, r := range s {
		if r.End <= cur {
			continue
		}
		if r.Off >= end {
			break
		}
		if r.Off > cur {
			out = append(out, Range{cur, r.Off})
		}
		cur = max(cur, r.End)
		if cur >= end {
			break
		}
	}
	if cur < end {
		out = append(out, Range{cur, end})
	}
	// Holes start and end on blocks too (refetching a few bytes present is
	// harmless), and holes separated by little present data join: one
	// request is cheaper.
	var joined Ranges
	for _, r := range out {
		if align > 1 {
			r.Off = r.Off / align * align
			r.End = min((r.End+align-1)/align*align, size)
		}
		if k := len(joined); k > 0 && r.Off-joined[k-1].End <= gap {
			joined[k-1].End = r.End
			continue
		}
		joined = append(joined, r)
	}
	return joined
}

// Split cuts a range into pieces of at most n bytes (progress, resumption).
func (r Range) Split(n int64) []Range {
	if n <= 0 || r.Len() <= n {
		return []Range{r}
	}
	var out []Range
	for o := r.Off; o < r.End; o += n {
		out = append(out, Range{o, min(o+n, r.End)})
	}
	return out
}
