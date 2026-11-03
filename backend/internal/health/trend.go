package health

import (
	"fmt"
	"strings"

	"strataos/internal/storage"
)

// Change is a SMART value that moved the wrong way since it was last known.
type Change struct {
	Field    string `json:"field"` // re | pe | un | crc | me | sp | wear
	From, To int64
}

// counter names, in the order of the messages; they are server texts.
var counters = []struct {
	field, name string
	get         func(storage.SmartSample) *int64
}{
	{"re", "reallocated sectors", func(s storage.SmartSample) *int64 { return s.Reallocated }},
	{"pe", "sectors pending reallocation", func(s storage.SmartSample) *int64 { return s.Pending }},
	{"un", "unreadable sectors", func(s storage.SmartSample) *int64 { return s.Uncorrectable }},
	{"crc", "cable (CRC) errors", func(s storage.SmartSample) *int64 { return s.CRCErrors }},
	{"me", "media errors", func(s storage.SmartSample) *int64 { return s.MediaErrors }},
}

// CounterName is the English name of a history field, for messages.
func CounterName(field string) string {
	for _, c := range counters {
		if c.field == field {
			return c.name
		}
	}
	return field
}

// Wear thresholds of SSDs that get a notification.
var wearSteps = []int64{80, 90}

// Trends compares a reading with the baseline, updates the baseline and
// returns what got worse: a counter that grew, spare cells that went down,
// an SSD wear that crossed 80 % or 90 %. A value seen for the first time
// is only remembered (the absolute alerts cover it), except the wear.
func Trends(baseline map[string]int64, s storage.SmartSample) []Change {
	var out []Change
	for _, c := range counters {
		v := c.get(s)
		if v == nil {
			continue
		}
		old, known := baseline[c.field]
		if known && *v > old {
			out = append(out, Change{Field: c.field, From: old, To: *v})
		}
		// A count that went down (pending sectors remapped) is remembered
		// too, so that a new rise is reported again.
		baseline[c.field] = *v
	}
	if s.Spare != nil {
		old, known := baseline["sp"]
		if known && *s.Spare < old {
			out = append(out, Change{Field: "sp", From: old, To: *s.Spare})
		}
		baseline["sp"] = *s.Spare
	}
	if s.PercentUsed != nil {
		step := int64(0)
		for _, w := range wearSteps {
			if *s.PercentUsed >= w {
				step = w
			}
		}
		if step > baseline["wear"] {
			out = append(out, Change{Field: "wear", From: baseline["wear"], To: *s.PercentUsed})
		}
		baseline["wear"] = step
	}
	return out
}

// ChangeMessage is the notification text of a change (server texts, with
// their French pair in frontend/src/i18n/pending/feat-health.json).
func ChangeMessage(device, model string, c Change) (level, msg string) {
	disk := strings.TrimSpace(device + " (" + model + ")")
	switch c.Field {
	case "sp":
		return "warning", sub("SSD {0}: spare cells went down from {1}% to {2}%: it is wearing out", disk, c.From, c.To)
	case "wear":
		if c.To >= 90 {
			return "error", sub("SSD {0} has used {1}% of its rated endurance: replace it soon", disk, c.To)
		}
		return "warning", sub("SSD {0} has used {1}% of its rated endurance: plan its replacement", disk, c.To)
	}
	return "warning", sub("Disk {0} is getting worse: {1} went from {2} to {3}. Check your backups", disk, CounterName(c.Field), c.From, c.To)
}

// sub fills {0}, {1}… in an English server text, the way the translation
// tables expect it.
func sub(format string, args ...any) string {
	for i, a := range args {
		format = strings.ReplaceAll(format, fmt.Sprintf("{%d}", i), fmt.Sprint(a))
	}
	return format
}
