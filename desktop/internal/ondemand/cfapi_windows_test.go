package ondemand

import (
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The functions this provider calls exist in this Windows' cldapi.dll.
func TestCldapiProcs(t *testing.T) {
	if ok, _ := supported(); !ok {
		t.Skip("no cldapi.dll")
	}
	for _, p := range []*windows.LazyProc{procCfRegisterSyncRoot, procCfConnectSyncRoot, procCfDisconnectSyncRoot, procCfGetSyncRootInfo,
		procCfExecute, procCfCreatePlaceholders, procCfUpdatePlaceholder, procCfConvertToPh, procCfSetPinState,
		procCfHydratePlaceholder, procCfDehydratePh, procCfGetPlaceholderInfo, procCfStateFromFindData, procFindFirstFileW, procFindNextFileW} {
		if err := p.Find(); err != nil {
			t.Errorf("%s: %v", p.Name, err)
		}
	}
	if _, err := windows.GetCurrentProcessToken().GetTokenUser(); err != nil {
		t.Fatal(err)
	}
	callbackTable() // the callbacks can be made
}

// TestCfapiEndToEnd registers a plain sync root in a temporary folder over
// the fake NAS: placeholders, hydration on read, a new file sent, a change
// of the NAS shown. Skipped where the Cloud Files driver is unavailable.
func TestCfapiEndToEnd(t *testing.T) {
	if ok, _ := supported(); !ok {
		t.Skip("no cldapi.dll")
	}
	nas := newNAS()
	nas.put("docs/report.txt", []byte("quarterly report"))
	s := newService(t, nas)
	waitFor(t, "listing", s.Ready)
	root := filepath.Join(t.TempDir(), "StrataOS - Test")
	pr, err := StartProvider(s, ProviderOptions{Root: root, Account: "test", Name: "StrataOS test"})
	if err != nil {
		t.Fatal(err)
	}
	p := pr.(*cfProvider)
	defer func() {
		pr.Close()
		path, _ := windows.UTF16PtrFromString(root)
		procCfUnregisterSyncRoot.Call(uintptr(unsafe.Pointer(path)))
	}()
	for i := 0; i < 200 && pr.Info().State != "ready"; i++ {
		if pr.Info().State == "error" {
			t.Skip("Cloud Files unavailable here: ", pr.Info().Error)
		}
		time.Sleep(50 * time.Millisecond)
	}
	file := filepath.Join(root, "Docs", "docs", "report.txt")
	st, err := os.Stat(file)
	if err != nil {
		t.Fatal("placeholder:", err)
	}
	if st.Size() != 16 || nas.read != 0 {
		t.Fatal("size", st.Size(), nas.read)
	}
	if a := p.attrs("Docs/docs/report.txt"); a&attrRecallOnDataAccess == 0 {
		t.Errorf("not a cloud file: attributes %#x", a)
	}
	b, err := os.ReadFile(file)
	if err != nil || string(b) != "quarterly report" {
		t.Fatal("hydration:", string(b), err)
	}
	if err := os.WriteFile(filepath.Join(root, "Docs", "new.txt"), []byte("from Windows"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "upload", func() bool { return string(nas.data("new.txt")) == "from Windows" })
	nas.put("docs/later.txt", []byte("x"))
	waitFor(t, "NAS change", func() bool { _, err := os.Stat(filepath.Join(root, "Docs", "docs", "later.txt")); return err == nil })
}
