package crypt

import (
	"strings"
	"testing"
)

func TestCipherDir(t *testing.T) {
	d, err := CipherDir("/srv/strata/main", "/srv/strata/main/Compta/2026")
	if err != nil || d != "/srv/strata/main/.strata-crypt/Compta__2026" {
		t.Errorf("CipherDir = %q, %v", d, err)
	}
	if _, err := CipherDir("/srv/strata/main", "/srv/strata/main"); err == nil {
		t.Error("volume root accepted")
	}
	if checkPass("short") == nil || checkPass("line\nbreak-long") == nil || checkPass("long enough") != nil {
		t.Error("passphrase rules")
	}
	k := "1A2B3C4D-1a2b3c4d-1a2b3c4d-1a2b3c4d-1a2b3c4d-1a2b3c4d-1a2b3c4d-1a2b3c4d"
	if n := normKey(k); !hexRe.MatchString(n) || formatKey(n) != strings.ToLower(k) {
		t.Errorf("recovery key format %q", formatKey(n))
	}
}
