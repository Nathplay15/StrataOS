package api

import (
	"strings"
	"testing"
)

func TestReportRedaction(t *testing.T) {
	in := strings.Join([]string{
		"smtp password=hunter2 user=bob",
		`{"smtpPassword": "s3cr3t!", "apiKey":"abc123"}`,
		"wpa_psk=MyWifiKey123",
		"Authorization: Bearer abcdefghijklmnop.qrstuv",
		"token stdev_0123456789abcdef0123",
		"hash $2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ0123",
		"root:$6$salt$longhashvalue:19000:0:99999:7:::",
		"rclone https://alice:pa55word@example.com/dav",
		"key 0123456789abcdef0123456789abcdef0123",
		"-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjE\n-----END OPENSSH PRIVATE KEY-----",
		"mail alice@example.org",
		"volume data is 93% full",
	}, "\n")
	out := redact(in, false)
	for _, secret := range []string{"hunter2", "s3cr3t", "abc123", "MyWifiKey123", "abcdefghijklmnop", "0123456789abcdef0123", "$2a$10$", "longhashvalue", "pa55word", "b3BlbnNzaC1rZXktdjE", "alice@example.org"} {
		if strings.Contains(out, secret) {
			t.Errorf("%q left in:\n%s", secret, out)
		}
	}
	for _, kept := range []string{"user=bob", "volume data is 93% full", "rclone https://alice:"} {
		if !strings.Contains(out, kept) {
			t.Errorf("%q lost:\n%s", kept, out)
		}
	}
	if !strings.Contains(redact("mail alice@example.org", true), "alice@example.org") {
		t.Error("e-mail removed although kept")
	}
}
