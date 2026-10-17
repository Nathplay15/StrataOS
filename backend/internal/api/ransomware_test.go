package api

import (
	"strings"
	"testing"
	"time"
)

func TestRansomDetection(t *testing.T) {
	w := newRansomWindow()
	now := time.Now()
	// Normal use: a few renames.
	for i := 0; i < 20; i++ {
		if f, _ := w.add("bob", "doc.txt", now.Add(time.Duration(i)*time.Second), 150); f {
			t.Fatal("normal activity flagged")
		}
	}
	// Mass encryption: renames to .locked.
	var reason string
	for i := 0; i < 12 && reason == ""; i++ {
		_, reason = w.add("eve", "photo.jpg.locked", now.Add(time.Duration(i)*time.Second), 150)
	}
	if !strings.Contains(reason, "extension") {
		t.Errorf("encryption not flagged: %q", reason)
	}
	// Flagged once per 10 minutes.
	if f, _ := w.add("eve", "x.locked", now.Add(20*time.Second), 150); f {
		t.Error("flagged twice")
	}
	// Mass deletion over the threshold.
	reason = ""
	for i := 0; i < 200 && reason == ""; i++ {
		_, reason = w.add("mallory", "f.txt", now.Add(time.Duration(i)*100*time.Millisecond), 150)
	}
	if !strings.Contains(reason, "en une minute") {
		t.Errorf("mass deletion not flagged: %q", reason)
	}
	// Spread over hours: not flagged.
	for i := 0; i < 400; i++ {
		if f, _ := w.add("slow", "f.txt", now.Add(time.Duration(i)*time.Minute), 150); f {
			t.Fatal("slow activity flagged")
		}
	}
	if u, ip, sh, tg, ok := parseAudit("alice|10.0.2.2|Photos|renameat|ok|a.jpg|a.jpg.locked"); !ok || u != "alice" || ip != "10.0.2.2" || sh != "Photos" || tg != "a.jpg.locked" {
		t.Errorf("parse: %v %v %v %v %v", u, ip, sh, tg, ok)
	}
	if _, _, _, _, ok := parseAudit("alice|10.0.2.2|Photos|openat|ok|a.jpg"); ok {
		t.Error("open counted")
	}
}
