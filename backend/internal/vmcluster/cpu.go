package vmcluster

import (
	"slices"
	"strings"
)

// A VM sees the CPU of its host by default (host-passthrough): the fastest,
// but live migration then needs members with the very same processor. A
// "cluster-safe CPU" is a named QEMU model every member can run, so VMs move
// between different processors (they lose the newest instructions).

// cpuModels ranks the x86 QEMU models from the oldest to the newest, Intel
// and AMD apart; the "-v1"-less names are those libvirt reports as usable.
var cpuModels = []string{
	"qemu64", "kvm64",
	"Nehalem", "Westmere", "SandyBridge", "IvyBridge", "Haswell-noTSX", "Haswell", "Broadwell-noTSX", "Broadwell",
	"Skylake-Client", "Skylake-Server", "Cascadelake-Server", "Icelake-Server", "Cooperlake", "SapphireRapids",
	"Opteron_G4", "Opteron_G5", "EPYC", "EPYC-IBPB", "EPYC-Rome", "EPYC-Milan", "EPYC-Genoa",
}

// rank orders models: unknown ones after qemu64 (usable but not trusted).
func rank(m string) int {
	if i := slices.Index(cpuModels, m); i >= 0 {
		return i
	}
	return -1
}

// CommonModel picks the newest known model usable on every member (each
// list is what `virsh domcapabilities` reports usable there). "" when the
// members have no known model in common (or no list).
func CommonModel(usable [][]string) string {
	if len(usable) == 0 {
		return ""
	}
	best, bestRank := "", -1
	for _, m := range usable[0] {
		r := rank(m)
		if r < 0 || r <= bestRank {
			continue
		}
		everywhere := true
		for _, l := range usable[1:] {
			if !slices.Contains(l, m) {
				everywhere = false
				break
			}
		}
		if everywhere {
			best, bestRank = m, r
		}
	}
	return best
}

// ValidModel checks a CPU model name (letters, digits, _ . -).
func ValidModel(m string) bool {
	if m == "" || len(m) > 40 {
		return false
	}
	return strings.IndexFunc(m, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-')
	}) < 0
}

// CPUCompatible says whether a VM with this CPU definition can run on the
// target: a named model must be usable there; the host's own CPU
// (host-passthrough or host-model, the default) needs the same processor
// (srcSig and dstSig describe the hosts: vendor, model and flags).
func CPUCompatible(mode, model string, targetUsable []string, srcSig, dstSig string) (bool, string) {
	switch mode {
	case "custom":
		if slices.Contains(targetUsable, model) {
			return true, ""
		}
		return false, "the target cannot run the CPU model " + model
	default:
		if srcSig != "" && srcSig == dstSig {
			return true, ""
		}
		return false, "this VM uses the processor of its NAS as it is, and the target has another processor: shut it down and choose the cluster-safe CPU model in its settings"
	}
}
