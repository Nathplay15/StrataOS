package webpush

import (
	"crypto/ecdh"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The example of RFC 8291 appendix A.
func TestEncryptRFC8291(t *testing.T) {
	asPriv, _ := b64.DecodeString("yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw")
	as, err := ecdh.P256().NewPrivateKey(asPriv)
	if err != nil {
		t.Fatal(err)
	}
	salt, _ := b64.DecodeString("DGv6ra1nlYgDCS1FRnbzlw")
	got, err := encrypt([]byte("When I grow up, I want to be a watermelon"),
		"BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4",
		"BTBZMqHH6r4Tts7J_aSIgg", salt, as)
	if err != nil {
		t.Fatal(err)
	}
	want := "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	if b64.EncodeToString(got) != want {
		t.Fatalf("got %s", b64.EncodeToString(got))
	}
}

func TestEncryptRejectsBadKeys(t *testing.T) {
	for _, c := range [][2]string{{"", "BTBZMqHH6r4Tts7J_aSIgg"}, {"BCVx", "BTBZMqHH6r4Tts7J_aSIgg"},
		{"BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4", "short"}} {
		if _, err := Encrypt([]byte("x"), c[0], c[1]); err == nil {
			t.Errorf("%v accepted", c)
		}
	}
	if _, err := Encrypt(make([]byte, MaxPlaintext+1), "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4", "BTBZMqHH6r4Tts7J_aSIgg"); err == nil {
		t.Error("oversized message accepted")
	}
}

func TestVAPIDAndSend(t *testing.T) {
	ks, err := NewVAPIDKey()
	if err != nil {
		t.Fatal(err)
	}
	v, err := ParseVAPID(ks)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.PublicKey()) != 87 { // 65 bytes in base64url
		t.Fatalf("public key %q", v.PublicKey())
	}
	var hdr http.Header
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr = r.Header
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		if r.URL.Path == "/gone" {
			w.WriteHeader(http.StatusGone)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	err = Send(srv.Client(), Message{Endpoint: srv.URL + "/up?x=1", Payload: []byte(`{"a":1}`), Urgency: "high"}, v, "mailto:nas@example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	if body != `{"a":1}` || hdr.Get("TTL") != "86400" || hdr.Get("Urgency") != "high" || hdr.Get("Content-Encoding") != "" {
		t.Fatalf("plain push: %q %v", body, hdr)
	}
	auth := hdr.Get("Authorization")
	jwt, _, ok := strings.Cut(strings.TrimPrefix(auth, "vapid t="), ", k=")
	if !ok || !v.verify(jwt) || !strings.HasSuffix(auth, "k="+v.PublicKey()) {
		t.Fatalf("authorization %q", auth)
	}
	err = Send(srv.Client(), Message{Endpoint: srv.URL + "/up", Payload: []byte("hi"),
		P256DH: "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4", Auth: "BTBZMqHH6r4Tts7J_aSIgg", TTL: time.Hour}, nil, "")
	if err != nil || hdr.Get("Content-Encoding") != "aes128gcm" || len(body) != 16+4+1+65+2+1+16 || hdr.Get("Authorization") != "" {
		t.Fatalf("encrypted push: %v %d %v", err, len(body), hdr)
	}
	if err := Send(srv.Client(), Message{Endpoint: srv.URL + "/gone"}, nil, ""); err != ErrGone {
		t.Fatalf("gone: %v", err)
	}
	if err := Send(srv.Client(), Message{Endpoint: "file:///etc/passwd"}, nil, ""); err == nil {
		t.Fatal("file URL accepted")
	}
}
