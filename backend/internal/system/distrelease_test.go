package system

import (
	"slices"
	"testing"
)

func TestParseRelease(t *testing.T) {
	r := ParseRelease("Origin: Debian\nLabel: Debian\nSuite: stable\nVersion: 15.1\nCodename: duke\nDate: x\nSHA256:\n abc 1 main/x\nVersion: 99\n")
	if r.Suite != "stable" || r.Codename != "duke" || r.Version != "15.1" || r.Major() != "15" || r.Point() != 1 {
		t.Fatalf("%+v", r)
	}
	if r := ParseRelease("Suite: testing\nCodename: forky\n"); r.Version != "" || r.Major() != "" || r.Point() != 0 {
		t.Fatalf("testing %+v", r)
	}
}

func TestTargetRelease(t *testing.T) {
	stable := func(c, v string) ArchiveRelease { return ArchiveRelease{Suite: "stable", Codename: c, Version: v} }
	testing_ := ArchiveRelease{Suite: "testing", Codename: "forky"}
	// The next stable is out but has had no point release yet: not ready.
	if g := TargetRelease("13", []ArchiveRelease{stable("forky", "14.0"), testing_}, false); g.Codename != "forky" || g.Number != "14" || g.Ready {
		t.Errorf("14.0: %+v", g)
	}
	if g := TargetRelease("13", []ArchiveRelease{stable("forky", "14.1")}, false); !g.Ready || g.Version != "14.1" {
		t.Errorf("14.1: %+v", g)
	}
	// Two releases later: the next one is oldstable by then.
	if g := TargetRelease("13", []ArchiveRelease{stable("duke", "15.2"), {Suite: "oldstable", Codename: "forky", Version: "14.9"}}, false); g.Codename != "forky" {
		t.Errorf("oldstable: %+v", g)
	}
	// Any later release, with a name StrataOS does not know yet.
	if g := TargetRelease("16", []ArchiveRelease{stable("newname", "17.3")}, false); g.Codename != "newname" || g.Number != "17" || !g.Ready {
		t.Errorf("17: %+v", g)
	}
	// Up to date: nothing.
	if g := TargetRelease("13", []ArchiveRelease{stable("trixie", "13.3"), testing_}, false); g.Codename != "" {
		t.Errorf("up to date: %+v", g)
	}
	// The testing override (tests): forky while it is testing.
	if g := TargetRelease("13", []ArchiveRelease{stable("trixie", "13.3"), testing_}, true); g.Codename != "forky" || g.Number != "14" || !g.Testing || !g.Ready {
		t.Errorf("testing: %+v", g)
	}
	RememberRelease("newname", "17")
	if ReleaseNumber("newname") != "17" || NextRelease("duke") != "" && ReleaseNumber(NextRelease("duke")) != "16" {
		t.Errorf("remembered: %q", ReleaseNumber("newname"))
	}
}

func TestParseSimulationAndForeign(t *testing.T) {
	if got := ParseSimulation("Inst libc6 [2.41] (2.42 Debian:testing)\nRemv strataos [0.13.0]\nRemv oldlib [1]\nConf libc6"); !slices.Equal(got, []string{"strataos", "oldlib"}) {
		t.Errorf("removed %v", got)
	}
	if got := ParseForeign("Listing...\nmylocal/now 1.0 amd64 [installed,local]\nbash/stable,now 5.2 amd64 [installed]\nother/now 2 all [installed,local]\n"); !slices.Equal(got, []string{"mylocal", "other"}) {
		t.Errorf("foreign %v", got)
	}
}

func TestChecksFailedAndForce(t *testing.T) {
	f := UpgradeFacts{From: "trixie", To: "forky", FreeRoot: 10 * GiB, FreeVar: -1, FreeBoot: -1, Moved: 2, RestoreMode: "btrfs", SimDone: true, StrataRemoved: true, KernelPending: "6.12.9"}
	ids := []string{}
	for _, c := range Failed(UpgradeChecks(f)) {
		ids = append(ids, c.ID)
		if c.Fix == "" {
			t.Errorf("%s has no fix", c.ID)
		}
	}
	if !slices.Equal(ids, []string{"kernel", "strataos"}) {
		t.Errorf("failed %v", ids)
	}
}
