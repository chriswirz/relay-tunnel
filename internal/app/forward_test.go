package app

import "testing"

func TestParseForward(t *testing.T) {
	cases := []struct {
		spec       string
		wantLocal  string
		wantRemote string
		wantErr    bool
	}{
		{"9000:example.com:80", "127.0.0.1:9000", "example.com:80", false},
		{"0.0.0.0:9000:10.0.0.5:22", "0.0.0.0:9000", "10.0.0.5:22", false},
		{"badspec", "", "", true},
		{"9000:host:80:extra:more", "", "", true},
	}
	for _, c := range cases {
		l, r, err := parseForward(c.spec)
		if (err != nil) != c.wantErr {
			t.Errorf("%q: err=%v wantErr=%v", c.spec, err, c.wantErr)
			continue
		}
		if !c.wantErr && (l != c.wantLocal || r != c.wantRemote) {
			t.Errorf("%q: got (%s,%s) want (%s,%s)", c.spec, l, r, c.wantLocal, c.wantRemote)
		}
	}
}

func TestDialPolicy(t *testing.T) {
	deny := HostPolicy{}
	if deny.dialAllowed("x:1") {
		t.Error("default policy should deny dial")
	}
	open := HostPolicy{AllowDial: true}
	if !open.dialAllowed("anything:1") {
		t.Error("AllowDial should permit any target")
	}
	wl := HostPolicy{AllowDial: true, Allow: []string{"a:1"}}
	if !wl.dialAllowed("a:1") || wl.dialAllowed("b:2") {
		t.Error("whitelist enforcement incorrect")
	}
}
