package docker

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func frame(stream byte, data string) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(data)))
	return append(h, data...)
}

func TestDemuxBoundsLines(t *testing.T) {
	var in bytes.Buffer
	in.Write(frame(1, "hello\nwor"))
	in.Write(frame(2, "err\n"))
	in.Write(frame(1, "ld\n"))
	// A container writing 1 MiB without a newline, in many frames.
	for i := 0; i < 64; i++ {
		in.Write(frame(1, strings.Repeat("x", 16*1024)))
	}
	in.Write(frame(1, "\nend\n"))
	var lines []string
	longest := 0
	if err := Demux(&in, false, func(s, l string) {
		lines = append(lines, s+":"+l[:min(len(l), 5)])
		longest = max(longest, len(l))
	}); err != nil {
		t.Fatal(err)
	}
	if longest > maxLogLine {
		t.Errorf("line of %d bytes forwarded, limit %d", longest, maxLogLine)
	}
	got := strings.Join(lines, ",")
	if !strings.HasPrefix(got, "stdout:hello,stderr:err,stdout:world,stdout:xxxxx") || !strings.HasSuffix(got, "stdout:end") {
		t.Errorf("unexpected lines: %s", got)
	}
}

func TestDemuxTruncatedFrame(t *testing.T) {
	in := frame(1, "complete\n")
	in = append(in, frame(1, "partial line that never ends")[:12]...)
	var n int
	if err := Demux(bytes.NewReader(in), false, func(string, string) { n++ }); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestRenderEnvRejectsEscapes(t *testing.T) {
	a := &App{Vars: []AppVar{{Key: "NAME", Label: "Name", Type: "text"}, {Key: "OTHER", Label: "Other", Type: "text"}}}
	for _, v := range []string{`x\`, "a\nB=1", `a"b`, "a$b", "a`b"} {
		if _, _, err := a.RenderEnv(map[string]string{"NAME": v, "OTHER": "y"}); err == nil {
			t.Errorf("%q accepted", v)
		}
	}
	env, _, err := a.RenderEnv(map[string]string{"NAME": "Mon NAS", "OTHER": "y"})
	if err != nil || env != "NAME=\"Mon NAS\"\nOTHER=\"y\"\n" {
		t.Errorf("got %q %v", env, err)
	}
}
