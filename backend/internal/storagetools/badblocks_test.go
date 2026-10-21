package storagetools

import (
	"strings"
	"testing"

	"strataos/internal/storage"
)

func status(s string) string { return s + strings.Repeat("\b", len(s)) }

func TestBadblocksReadOnly(t *testing.T) {
	p := NewBadblocksProgress(0)
	p.Feed("Checking blocks 0 to 2621439\nChecking for bad blocks (read-only test): ")
	p.Feed(status("  0.00% done, 0:00 elapsed. (0/0/0 errors)"))
	// A status line split between two reads.
	p.Feed(" 42.5")
	p.Feed(status("0% done, 0:12 elapsed. (1/0/0 errors)"))
	if p.Overall() != 42.5 || p.ReadErrors != 1 {
		t.Fatalf("progress %v errors %d", p.Overall(), p.ReadErrors)
	}
	p.Feed("done                                                 \nPass completed, 3 bad blocks found. (3/0/0 errors)\n")
	if !p.Completed || p.BadBlocks != 3 || p.Overall() != 100 {
		t.Fatalf("%+v", p)
	}
}

func TestBadblocksWrite(t *testing.T) {
	p := NewBadblocksProgress(4)
	p.Feed("Checking for bad blocks in read-write mode\nFrom block 0 to 1000\nTesting with pattern 0xaa: ")
	p.Feed(status(" 50.00% done, 0:01 elapsed. (0/0/0 errors)"))
	if got := p.Overall(); got != 6.25 {
		t.Fatalf("phase 1 at 50%%: %v", got)
	}
	p.Feed("done                                                 \nReading and comparing: " + status(" 50.00% done, 0:02 elapsed. (0/0/2 errors)"))
	if got := p.Overall(); got != 18.75 || p.CorruptErrors != 2 || p.Errors() != 2 {
		t.Fatalf("phase 2 at 50%%: %v %+v", got, p)
	}
	for _, pat := range []string{"0x55", "0xff", "0x00"} {
		p.Feed("done\nTesting with pattern " + pat + ": done\nReading and comparing: ")
	}
	p.Feed(status(" 99.00% done, 0:09 elapsed. (0/0/2 errors)"))
	if p.Phase != 8 || p.Overall() <= 99 {
		t.Fatalf("last phase: %+v %v", p, p.Overall())
	}
}

func TestParseSelfTest(t *testing.T) {
	running := parseSelfTest([]byte(`{"ata_smart_data":{"self_test":{"status":{"value":249,"string":"in progress, 90% remaining","remaining_percent":90},"polling_minutes":{"short":2,"extended":120}}}}`))
	if !running.Running || running.Remaining != 90 || running.ShortMin != 2 || running.LongMin != 120 {
		t.Fatalf("%+v", running)
	}
	done := parseSelfTest([]byte(`{"ata_smart_data":{"self_test":{"status":{"value":0,"string":"completed without error","passed":true}}},"ata_smart_self_test_log":{"standard":{"table":[{"status":{"string":"Completed: read failure","passed":false}}]}}}`))
	if done.Running || done.Passed == nil || *done.Passed || done.Result != "Completed: read failure" {
		t.Fatalf("%+v", done)
	}
	nvme := parseSelfTest([]byte(`{"nvme_self_test_log":{"current_self_test_operation":{"value":1},"current_self_test_completion_percent":30,"table":[{"self_test_result":{"value":0,"string":"Completed without error"}}]}}`))
	if !nvme.Running || nvme.Remaining != 70 || nvme.Passed == nil || !*nvme.Passed {
		t.Fatalf("%+v", nvme)
	}
	if parseSelfTest([]byte("garbage")).Running {
		t.Fatal("garbage")
	}
}

func TestCompareSmart(t *testing.T) {
	attrs := func(realloc, pending, crc string) *storage.SmartInfo {
		ok := true
		return &storage.SmartInfo{Passed: &ok, Attributes: []storage.SmartAttr{{ID: 5, Raw: realloc}, {ID: 197, Raw: pending}, {ID: 199, Raw: crc}, {ID: 9, Raw: "100"}}}
	}
	a, p, w := CompareSmart(attrs("0", "0", "0"), attrs("0", "0", "0"))
	if len(a) != 3 || len(p) != 0 || len(w) != 0 {
		t.Fatalf("clean: %v %v %v", a, p, w)
	}
	_, p, w = CompareSmart(attrs("0", "0", "1"), attrs("8", "2", "3"))
	if len(p) != 2 || len(w) != 1 || !strings.Contains(p[0], "0 → 8") {
		t.Fatalf("worse: %v %v", p, w)
	}
	// Pending sectors already there before the test still fail a new disk.
	_, p, _ = CompareSmart(attrs("0", "4", "0"), attrs("0", "4", "0"))
	if len(p) != 1 {
		t.Fatalf("pending: %v", p)
	}
}

// With -p, each pass starts with the same banner: the run is complete only
// after the last "Pass completed".
func TestBadblocksRepeatedPasses(t *testing.T) {
	p := NewBadblocksProgress(0)
	p.Feed("Checking for bad blocks (read-only test): done\nPass completed, 0 bad blocks found. (0/0/0 errors)\n")
	if !p.Completed {
		t.Fatal("first pass")
	}
	p.Feed("Checking for bad blocks (read-only test): " + status(" 10.00% done, 0:01 elapsed. (0/0/0 errors)"))
	if p.Completed || p.Overall() != 10 {
		t.Fatalf("second pass: %+v", p)
	}
}
