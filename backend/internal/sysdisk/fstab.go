package sysdisk

import (
	"slices"
	"strings"
)

// FstabEntry is a line of /etc/fstab.
type FstabEntry struct {
	Spec string // UUID=…, PARTUUID=…, LABEL=…, /dev/…
	File string // mount point ("none" or "swap" for swap)
	Type string
	Opts string
}

// ParseFstab reads the entries of an fstab (comments skipped).
func ParseFstab(text string) []FstabEntry {
	var out []FstabEntry
	for _, l := range strings.Split(text, "\n") {
		f := strings.Fields(l)
		if len(f) < 3 || strings.HasPrefix(f[0], "#") {
			continue
		}
		e := FstabEntry{Spec: f[0], File: f[1], Type: f[2]}
		if len(f) > 3 {
			e.Opts = f[3]
		}
		out = append(out, e)
	}
	return out
}

// FstabEdit says how the fstab of a copied system changes.
type FstabEdit struct {
	Root    string   // new source of "/" (UUID=…)
	SameFS  []string // mount points on the root file system (Btrfs subvolumes): same new source
	Merged  []string // mount points copied into the root: their lines go
	ESP     string   // new source of /boot/efi ("": the line goes)
	OldSwap []string // sources of the old swap partitions or arrays
	Swap    string   // new swap source ("": the old swap lines go)
}

// RewriteFstab applies e to an fstab; lines that go are kept as comments.
// Data volumes and anything else are left as they are.
func RewriteFstab(text string, e FstabEdit) string {
	lines := strings.Split(text, "\n")
	swapDone := false
	gone := func(l string) string { return "# " + l + "  # removed by StrataOS (system disk change)" }
	for i, l := range lines {
		f := strings.Fields(l)
		if len(f) < 3 || strings.HasPrefix(f[0], "#") {
			continue
		}
		file, typ := f[1], f[2]
		switch {
		case file == "/" || slices.Contains(e.SameFS, file):
			lines[i] = replaceSpec(l, e.Root)
		case slices.Contains(e.Merged, file):
			lines[i] = gone(l)
		case file == "/boot/efi":
			if e.ESP != "" {
				lines[i] = replaceSpec(l, e.ESP)
			} else {
				lines[i] = gone(l)
			}
		case typ == "swap" && slices.Contains(e.OldSwap, f[0]):
			if e.Swap != "" && !swapDone {
				lines[i] = replaceSpec(l, e.Swap)
				swapDone = true
			} else {
				lines[i] = gone(l)
			}
		}
	}
	out := strings.Join(lines, "\n")
	if e.Swap != "" && !swapDone {
		if !strings.HasSuffix(out, "\n") && out != "" {
			out += "\n"
		}
		out += e.Swap + " none swap sw 0 0\n"
	}
	return out
}

// replaceSpec swaps the first field of an fstab line, keeping the spacing.
func replaceSpec(line, spec string) string {
	start := len(line) - len(strings.TrimLeft(line, " \t"))
	rest := line[start:]
	end := strings.IndexAny(rest, " \t")
	if end < 0 {
		return line
	}
	return line[:start] + spec + rest[end:]
}
