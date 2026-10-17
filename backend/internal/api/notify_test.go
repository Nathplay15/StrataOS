package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTelegram(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			_ = json.NewDecoder(r.Body).Decode(&got)
			io.WriteString(w, `{"ok":true}`)
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			io.WriteString(w, `{"ok":true,"result":[{"message":{"chat":{"id":42,"type":"private","first_name":"Nat","username":"nat"}}},{"message":{"chat":{"id":42,"type":"private"}}}]}`)
		}
	}))
	defer srv.Close()
	telegramBase = srv.URL
	tok := "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	if err := sendTelegram(tok, "42", "<b>x</b>"); err != nil {
		t.Fatal(err)
	}
	if got["chat_id"] != "42" || got["parse_mode"] != "HTML" {
		t.Errorf("payload: %v", got)
	}
	chats, err := telegramChats(tok)
	if err != nil || len(chats) != 1 || chats[0]["id"] != "42" {
		t.Errorf("chats: %v %v", chats, err)
	}
	if err := sendTelegram("bad/../token", "42", "x"); err == nil {
		t.Error("malformed token accepted")
	}
}

func TestNtfy(t *testing.T) {
	var title, body, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		title, auth = r.Header.Get("Title"), r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		body = string(b)
	}))
	defer srv.Close()
	if err := sendNtfy(srv.URL+"/alerts", "tk", "[nas] Disque", "message"); err != nil {
		t.Fatal(err)
	}
	if body != "message" || auth != "Bearer tk" || !strings.Contains(title, "nas") {
		t.Errorf("got %q %q %q", title, body, auth)
	}
	if sendNtfy(srv.URL, "", "x", "y") == nil {
		t.Error("URL without topic accepted")
	}
}
