package store

import "testing"

func TestLoginNewDevice(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// First successful sign-in from a browser: new device.
	l := s.Login("bob", "192.0.2.2", "TestBrowser/1.0 (Linux)", true, "password")
	if !l.NewDevice || !l.OK {
		t.Fatalf("first sign-in: %+v, want OK + NewDevice", l)
	}
	// Same browser again: known device.
	if l = s.Login("bob", "192.0.2.2", "TestBrowser/1.0 (Linux)", true, "passkey"); l.NewDevice {
		t.Fatalf("same browser: %+v, want no NewDevice", l)
	}
	// Another browser, same account: new device again.
	if l = s.Login("bob", "192.0.2.7", "OtherBrowser/9 (Phone)", true, "password"); !l.NewDevice {
		t.Fatalf("other browser: %+v, want NewDevice", l)
	}
	// Another account with the first browser: its own device list.
	if l = s.Login("alice", "192.0.2.2", "TestBrowser/1.0 (Linux)", true, "password"); !l.NewDevice {
		t.Fatalf("alice on bob's browser: %+v, want NewDevice (per account)", l)
	}
	// A failed attempt never marks the device as known.
	s.Login("bob", "192.0.2.9", "EvilBrowser/1", false, "password")
	if l = s.Login("bob", "192.0.2.9", "EvilBrowser/1", true, "password"); !l.NewDevice {
		t.Fatalf("after a failed attempt the device must still be new: %+v", l)
	}
	// The User-Agent is truncated.
	long := make([]byte, 500)
	for i := range long {
		long[i] = 'x'
	}
	if l = s.Login("bob", "192.0.2.2", string(long), true, "password"); len(l.UA) != maxUA {
		t.Fatalf("UA length = %d, want %d", len(l.UA), maxUA)
	}

	var st *State
	s.Read(func(x *State) { st = x })
	if got := len(st.Devices["bob"]); got != 4 { // 3 browsers + the truncated long UA
		t.Fatalf("devices of bob = %d, want 4", got)
	}

	// The device list is bounded.
	for i := 0; i < maxDevicesPerUser+5; i++ {
		s.Login("bob", "192.0.2.2", "UA-"+string(rune('a'+i%26))+"-"+string(rune('0'+i/26)), true, "password")
	}
	s.Read(func(x *State) { st = x })
	if got := len(st.Devices["bob"]); got != maxDevicesPerUser {
		t.Fatalf("devices of bob = %d, want bounded to %d", got, maxDevicesPerUser)
	}

	// LoginsOf: newest first, per account, ok filter.
	all := st.LoginsOf("", 500, false)
	if len(all) == 0 || all[0].User != "bob" {
		t.Fatalf("LoginsOf newest first: %+v", all[:1])
	}
	if n := len(st.LoginsOf("alice", 500, false)); n != 1 {
		t.Fatalf("alice logins = %d, want 1", n)
	}
	if n := len(st.LoginsOf("bob", 500, true)); n == 0 {
		t.Fatal("bob has successful logins, ok filter returned none")
	}
	for _, l := range st.LoginsOf("bob", 500, true) {
		if !l.OK {
			t.Fatalf("ok filter kept a failed attempt: %+v", l)
		}
	}
	// The history is bounded too.
	for i := 0; i < maxLogins+10; i++ {
		s.Login("bob", "192.0.2.2", "UA", false, "password")
	}
	s.Read(func(x *State) { st = x })
	if len(st.Logins) != maxLogins {
		t.Fatalf("logins = %d, want bounded to %d", len(st.Logins), maxLogins)
	}
}
