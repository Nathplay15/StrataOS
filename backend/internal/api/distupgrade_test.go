package api

import "testing"

func TestDistSourcesBackupNames(t *testing.T) {
	p := "/etc/apt/sources.list.d/debian.sources"
	if got, err := hexDecodePath(hexEncodePath(p)); err != nil || got != p {
		t.Fatal(got, err)
	}
	for _, bad := range []string{hexEncodePath("/etc/passwd"), hexEncodePath("/etc/apt/../shadow"), "zz"} {
		if _, err := hexDecodePath(bad); err == nil {
			t.Fatal("accepted", bad)
		}
	}
}
