package ondemand

import (
	"encoding/json"
	"reflect"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func TestAgentJSON(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the agent.json folders are the macOS app group ones")
	}
	var v struct {
		Port    int
		Token   string
		Folders []string
	}
	b := AgentJSON(4242, "tok", []string{"/Users/a/Sync/", "relative", "/Users/a/Sync", "/Users/a/Library/CloudStorage/StrataOS-NAS"})
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if v.Port != 4242 || v.Token != "tok" || !reflect.DeepEqual(v.Folders, []string{"/Users/a/Library/CloudStorage/StrataOS-NAS", "/Users/a/Sync"}) {
		t.Fatalf("%+v", v)
	}
	if string(AgentJSON(1, "t", nil)) != `{"folders":[],"port":1,"token":"t"}` {
		t.Fatal(string(AgentJSON(1, "t", nil)))
	}
}

func TestHelperRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the helper and its output are macOS ones")
	}
	if p, err := HelperRoot("warning\n/Users/a/Library/CloudStorage/StrataOS-NAS\n"); err != nil || p != "/Users/a/Library/CloudStorage/StrataOS-NAS" {
		t.Fatal(p, err)
	}
	for _, out := range []string{"", "\n", "not a path"} {
		if _, err := HelperRoot(out); err == nil {
			t.Fatal(out)
		}
	}
}

func TestDebouncer(t *testing.T) {
	var n atomic.Int32
	d := &debouncer{delay: 50 * time.Millisecond, f: func() { n.Add(1) }}
	for range 10 {
		d.call()
	}
	time.Sleep(150 * time.Millisecond)
	d.call()
	time.Sleep(150 * time.Millisecond)
	if n.Load() != 2 {
		t.Fatal(n.Load())
	}
}
