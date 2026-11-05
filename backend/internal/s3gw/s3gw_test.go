package s3gw

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBucketNames(t *testing.T) {
	for _, ok := range []string{"photos", "my-share.2024", "abc"} {
		if err := ValidBucketName(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"ab", "Photos", "-photos", "photos-", "a..b", "a.-b", "192.168.1.10", "xn--abc", "x-s3alias", "a_b", strings.Repeat("a", 64)} {
		if ValidBucketName(bad) == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	for in, want := range map[string]string{"Photos": "photos", "Mes Documents": "mes-documents", "A": "a-0", "__x__y": "x-y", "Été": "t"} {
		got := BucketName(in)
		if got != want && !(in == "Été" && ValidBucketName(got) == nil) {
			t.Errorf("BucketName(%q) = %q, want %q", in, got, want)
		}
		if ValidBucketName(got) != nil {
			t.Errorf("BucketName(%q) = %q is not valid", in, got)
		}
	}
	if ValidDomain("s3.nas.example.com") != nil || ValidDomain("") != nil || ValidDomain("bad_domain") == nil || ValidDomain("nas") == nil {
		t.Fatal("domains")
	}
}

func TestACLAndIAM(t *testing.T) {
	var acl struct {
		Owner    string
		Grantees []struct{ Permission, Access, Type string }
	}
	if err := json.Unmarshal(ACLJSON("root", []Grant{{"KEYB", false}, {"KEYA", true}}), &acl); err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, g := range acl.Grantees {
		got = append(got, g.Permission+":"+g.Access+":"+g.Type)
	}
	if acl.Owner != "root" || strings.Join(got, " ") != "READ:KEYA:CanonicalUser WRITE:KEYA:CanonicalUser READ:KEYB:CanonicalUser" {
		t.Fatalf("acl: %v %v", acl.Owner, got)
	}
	if b := ACLJSON("root", nil); string(b) != `{"Owner":"root","Grantees":[]}` {
		t.Fatalf("empty acl: %s", b)
	}
	var iam struct {
		AccessAccounts map[string]struct {
			Access, Secret, Role string
			UserID, GroupID      int
		}
	}
	_ = json.Unmarshal(IAMJSON([]Account{{"AK1", "s1", 1001, 100}}), &iam)
	if a := iam.AccessAccounts["AK1"]; a.Secret != "s1" || a.Role != "user" || a.UserID != 1001 || a.GroupID != 100 {
		t.Fatalf("iam: %+v", iam)
	}
	for perm, want := range map[string][2]bool{"rw": {true, true}, "ro": {true, false}, "none": {}, "": {}} {
		if r, w := Perm(perm); r != want[0] || w != want[1] {
			t.Errorf("Perm(%q)", perm)
		}
	}
}

func TestUnitFile(t *testing.T) {
	u := UnitFile(Options{Port: 7380, Domain: "s3.nas.example", CertFile: "/c.pem", KeyFile: "/k.pem", CertID: "abc"})
	for _, want := range []string{"--port :7380", "--cert /c.pem --key /k.pem", "--virtual-domain s3.nas.example", "posix --bucketlinks --chuid --chgid --sidecar " + MetaDir + " " + BucketsDir, "EnvironmentFile=" + EnvFile, "--iam-cache-disable", "# Certificate: abc"} {
		if !strings.Contains(u, want) {
			t.Errorf("unit lacks %q", want)
		}
	}
	if strings.Contains(UnitFile(Options{Port: 7380}), "--cert") {
		t.Error("plain HTTP unit with a certificate")
	}
	if strings.Contains(u, "ROOT_SECRET") {
		t.Error("secret on the command line")
	}
}

func TestParseLog(t *testing.T) {
	lines := `rootaccess photos [05/November/2026:19:21:32 +0100] 127.0.0.1 AKBOB 3CW8 s3_GetObject a.txt /photos/a.txt 200 - 0 0 0 0 - rclone/ - x SigV4 - AuthHeader s3 - arn Yes
rootaccess media [05/November/2026:19:21:33 +0100] 10.0.0.2 AKALICE C4ST s3_ListObjects - /media?prefix= 403 AccessDenied 0 0 0 0 - rclone/ - x SigV4 - AuthHeader s3 - arn Yes
garbage line
`
	es := Tail(strings.NewReader(lines), 10)
	if len(es) != 2 {
		t.Fatalf("%d entries", len(es))
	}
	if e := es[0]; e.Op != "ListObjects" || e.Status != 403 || e.Error != "AccessDenied" || e.Key != "" || e.Access != "AKALICE" || e.Bucket != "media" || e.IP != "10.0.0.2" {
		t.Fatalf("%+v", e)
	}
	if e := es[1]; e.Op != "GetObject" || e.Key != "a.txt" || e.Status != 200 || e.Time.Minute() != 21 {
		t.Fatalf("%+v", e)
	}
	if es := Tail(strings.NewReader(lines), 1); len(es) != 1 || es[0].Bucket != "media" {
		t.Fatal("tail limit")
	}
}
