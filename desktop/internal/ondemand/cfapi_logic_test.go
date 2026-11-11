package ondemand

import (
	"reflect"
	"testing"
	"unsafe"
)

func TestCfLayouts(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("64-bit layouts")
	}
	sizes := map[string][2]uintptr{
		"CF_CALLBACK_INFO":           {unsafe.Sizeof(cfCallbackInfo{}), 152},
		"CF_CALLBACK_PARAMETERS":     {unsafe.Sizeof(cfCallbackParams{}), 48},
		"CF_OPERATION_INFO":          {unsafe.Sizeof(cfOperationInfo{}), 48},
		"CF_OPERATION_PARAMETERS":    {unsafe.Sizeof(cfOperationParams{}), 48},
		"CF_FS_METADATA":             {unsafe.Sizeof(cfFSMetadata{}), 48},
		"CF_PLACEHOLDER_CREATE_INFO": {unsafe.Sizeof(cfPlaceholderCreateInfo{}), 88},
		"CF_SYNC_REGISTRATION":       {unsafe.Sizeof(cfSyncRegistration{}), 72},
		"CF_SYNC_POLICIES":           {unsafe.Sizeof(cfSyncPolicies{}), 24},
		"CF_CALLBACK_REGISTRATION":   {unsafe.Sizeof(cfCallbackRegistration{}), 16},
		"WIN32_FIND_DATAW":           {unsafe.Sizeof(win32FindData{}), 592},
	}
	for n, s := range sizes {
		if s[0] != s[1] {
			t.Errorf("%s: %d bytes, want %d", n, s[0], s[1])
		}
	}
	var ci cfCallbackInfo
	offs := map[string][2]uintptr{
		"ConnectionKey":       {unsafe.Offsetof(ci.ConnectionKey), 8},
		"VolumeDosName":       {unsafe.Offsetof(ci.VolumeDosName), 32},
		"FileSize":            {unsafe.Offsetof(ci.FileSize), 80},
		"FileIdentity":        {unsafe.Offsetof(ci.FileIdentity), 88},
		"NormalizedPath":      {unsafe.Offsetof(ci.NormalizedPath), 104},
		"TransferKey":         {unsafe.Offsetof(ci.TransferKey), 112},
		"ProcessInfo":         {unsafe.Offsetof(ci.ProcessInfo), 136},
		"RequestKey":          {unsafe.Offsetof(ci.RequestKey), 144},
		"params.Flags":        {unsafe.Offsetof(cfCallbackParams{}.Flags), 8},
		"params.A":            {unsafe.Offsetof(cfCallbackParams{}.A), 16},
		"op.TransferKey":      {unsafe.Offsetof(cfOperationInfo{}.TransferKey), 16},
		"op.RequestKey":       {unsafe.Offsetof(cfOperationInfo{}.RequestKey), 40},
		"opp.Buffer":          {unsafe.Offsetof(cfOperationParams{}.Buffer), 16},
		"opp.Length":          {unsafe.Offsetof(cfOperationParams{}.Length), 32},
		"create.FileIdentity": {unsafe.Offsetof(cfPlaceholderCreateInfo{}.FileIdentity), 56},
		"create.CreateUsn":    {unsafe.Offsetof(cfPlaceholderCreateInfo{}.CreateUsn), 80},
		"reg.ProviderID":      {unsafe.Offsetof(cfSyncRegistration{}.ProviderID), 52},
		"find.FileName":       {unsafe.Offsetof(win32FindData{}.FileName), 44},
	}
	for n, o := range offs {
		if o[0] != o[1] {
			t.Errorf("%s at %d, want %d", n, o[0], o[1])
		}
	}
	for v, want := range map[int32]uint32{statusUnsuccessful: 0xC0000001, statusAccessDenied: 0xC0000022, statusNotSameDevice: 0xC00000D4, statusCloudUnavailable: 0xC000CF07} {
		if uint32(v) != want {
			t.Errorf("status %#x, want %#x", uint32(v), want)
		}
	}
}

func TestCfLogic(t *testing.T) {
	if SyncRootID("S-1-5-21-1", "main") != "StrataOS!S-1-5-21-1!main" {
		t.Fatal("id")
	}
	for _, c := range []struct {
		dir bool
		v   string
	}{{true, ""}, {false, "abc-12"}, {false, ""}} {
		d, v, ok := decodeIdentity(encodeIdentity(c.dir, c.v))
		if !ok || d != c.dir || v != c.v {
			t.Fatal("identity", c)
		}
	}
	if _, _, ok := decodeIdentity([]byte("junk")); ok {
		t.Fatal("junk identity")
	}
	// Ranges: 4 KiB blocks, the end of the file, pieces.
	if got := transferChunks(100, 10, 10000, 1<<20); !reflect.DeepEqual(got, []Range{{0, 4096}}) {
		t.Fatal(got)
	}
	if got := transferChunks(5000, -1, 10000, 4096); !reflect.DeepEqual(got, []Range{{4096, 8192}, {8192, 10000}}) {
		t.Fatal(got)
	}
	if got := transferChunks(0, 20000, 10000, 6000); !reflect.DeepEqual(got, []Range{{0, 4096}, {4096, 8192}, {8192, 10000}}) {
		t.Fatal(got)
	}
	if transferChunks(10000, 5, 10000, 4096) != nil || transferChunks(0, 5, 0, 4096) != nil {
		t.Fatal("empty")
	}
	if r, ok := winRel(`C:\Users\me\StrataOS - NAS`, `c:\users\me\strataos - nas\Docs\a.txt`); !ok || r != "Docs/a.txt" {
		t.Fatal(r, ok)
	}
	if r, ok := winRel(`C:\Users\me\StrataOS - NAS\`, `C:\Users\me\StrataOS - NAS`); !ok || r != "" {
		t.Fatal(r, ok)
	}
	if _, ok := winRel(`C:\Users\me\StrataOS`, `C:\Users\me\StrataOS - NAS\a`); ok {
		t.Fatal("prefix of another folder")
	}
	if !ignoredLocal("~$report.docx") || !ignoredLocal("desktop.ini") || ignoredLocal("report.docx") {
		t.Fatal("ignored")
	}
}

func TestPlanDir(t *testing.T) {
	remote := []Item{
		{Name: "new.txt", Version: "1"},
		{Name: "same.txt", Version: "1"},
		{Name: "behind.txt", Version: "2"},
		{Name: "edited.txt", Version: "1"},
		{Name: "Sub", Dir: true},
		{Name: "SUB", Dir: true}, // same name for Windows: left out
		{Name: "kind", Dir: true},
	}
	local := []localEntry{
		{Name: "same.txt", Placeholder: true, InSync: true, Version: "1"},
		{Name: "behind.txt", Placeholder: true, InSync: true, Version: "1"},
		{Name: "edited.txt", Placeholder: true, InSync: false, Version: "1"},
		{Name: "sub", Dir: true, Placeholder: true, InSync: true},
		{Name: "kind"}, // a file here, a folder there
		{Name: "gone.txt", Placeholder: true, InSync: true, Version: "1"},
		{Name: "mine.txt"},
		{Name: "MyDir", Dir: true},
		{Name: "unsent.txt", Placeholder: true, InSync: false, Version: "3"},
	}
	got := map[string]dirOpKind{}
	var kinds []dirOpKind
	for _, op := range planDir(local, remote) {
		if op.Name == "MyDir" {
			kinds = append(kinds, op.Kind)
			continue
		}
		if _, dup := got[op.Name]; dup {
			t.Fatal("twice:", op.Name)
		}
		got[op.Name] = op.Kind
	}
	want := map[string]dirOpKind{
		"new.txt": opCreate, "behind.txt": opUpdate, "edited.txt": opUpload, "sub": opRecurse,
		"gone.txt": opDeleteHere, "mine.txt": opUpload, "unsent.txt": opUpload,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plan:\n got %v\nwant %v", got, want)
	}
	if !reflect.DeepEqual(kinds, []dirOpKind{opMkdirThere, opRecurse}) {
		t.Fatal("local folder", kinds)
	}
}
