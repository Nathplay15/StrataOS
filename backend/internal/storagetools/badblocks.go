package storagetools

import (
	"regexp"
	"strconv"
	"strings"
)

// BadblocksProgress follows the status line badblocks -s prints on stderr
// (rewritten in place with backspaces): "Testing with pattern 0xaa: 12.34% done,
// 0:10 elapsed. (0/0/0 errors)". A destructive test (-w) has two phases per
// pattern (write, then read and compare); a read-only test has one.
type BadblocksProgress struct {
	Phases  int
	Phase   int     // current phase, from 1
	Percent float64 // of the current phase
	// Read, write and corruption errors counted so far.
	ReadErrors, WriteErrors, CorruptErrors int
	// BadBlocks is set from the final "Pass completed, N bad blocks found." line.
	BadBlocks int64
	Completed bool
	pending   string
}

var (
	bbStatusRe = regexp.MustCompile(`(\d+(?:\.\d+)?)% done, [\d:]+ elapsed\. \((\d+)/(\d+)/(\d+) errors\)`)
	bbPassRe   = regexp.MustCompile(`Pass completed, (\d+) bad blocks found`)
)

// NewBadblocksProgress prepares a parser for a test of patterns patterns (0: read-only).
func NewBadblocksProgress(patterns int) *BadblocksProgress {
	if patterns <= 0 {
		return &BadblocksProgress{Phases: 1}
	}
	return &BadblocksProgress{Phases: 2 * patterns}
}

// Feed parses a chunk of badblocks' stderr (chunks may split a status line).
func (p *BadblocksProgress) Feed(chunk string) {
	s := p.pending + chunk
	cut := strings.LastIndexAny(s, "\b\r\n")
	if cut < 0 {
		p.pending = s
		return
	}
	p.pending = s[cut+1:]
	for _, tok := range strings.FieldsFunc(s[:cut+1], func(r rune) bool { return r == '\b' || r == '\r' || r == '\n' }) {
		p.token(tok)
	}
}

func (p *BadblocksProgress) token(t string) {
	switch {
	case strings.Contains(t, "Testing with pattern"), strings.Contains(t, "Reading and comparing"):
		p.Phase++
		p.Percent = 0
	case strings.Contains(t, "Checking for bad blocks (read-only test)"):
		// Also the start of each repeated pass (-p).
		p.Phase = 1
		p.Percent = 0
		p.Completed = false
	}
	if p.Phase > p.Phases {
		p.Phase = p.Phases
	}
	if m := bbStatusRe.FindStringSubmatch(t); m != nil {
		if p.Phase == 0 {
			p.Phase = 1
		}
		p.Percent, _ = strconv.ParseFloat(m[1], 64)
		p.ReadErrors, _ = strconv.Atoi(m[2])
		p.WriteErrors, _ = strconv.Atoi(m[3])
		p.CorruptErrors, _ = strconv.Atoi(m[4])
	}
	if m := bbPassRe.FindStringSubmatch(t); m != nil {
		p.BadBlocks, _ = strconv.ParseInt(m[1], 10, 64)
		p.Completed = true
		p.Phase, p.Percent = p.Phases, 100
	}
}

// Overall is the progress of the whole badblocks run, 0..100.
func (p *BadblocksProgress) Overall() float64 {
	if p.Phase == 0 {
		return 0
	}
	v := (float64(p.Phase-1) + p.Percent/100) / float64(p.Phases) * 100
	if v > 100 {
		v = 100
	}
	return v
}

// Errors is the total of the errors counted so far.
func (p *BadblocksProgress) Errors() int { return p.ReadErrors + p.WriteErrors + p.CorruptErrors }
