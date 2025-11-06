package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingDefaultIsEmpty(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.json"), false)
	if err != nil {
		t.Fatalf("missing default path should not error: %v", err)
	}
	if c.Expose.Server != "" || c.Connect.Code != "" {
		t.Errorf("expected empty config, got %+v", c)
	}
}

func TestLoadMissingExplicitErrors(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.json"), true); err == nil {
		t.Fatal("explicitly-requested missing file should error")
	}
}

func TestLoadParses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	os.WriteFile(path, []byte(`{
		"server": { "http": ":9000", "udp": ":9001" },
		"expose": { "server": "ws://h:7000/signal", "code": "abc", "allow": ["127.0.0.1:80"] },
		"connect": { "server": "ws://h:7000/signal", "code": "abc", "forward": ["9000:127.0.0.1:80"] }
	}`), 0o644)
	c, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Server.HTTP != ":9000" || c.Server.UDP != ":9001" {
		t.Errorf("bad server section parse: %+v", c.Server)
	}
	if c.Expose.Server != "ws://h:7000/signal" || c.Expose.Code != "abc" {
		t.Errorf("bad expose section parse: %+v", c.Expose)
	}
	if len(c.Expose.Allow) != 1 || c.Expose.Allow[0] != "127.0.0.1:80" {
		t.Errorf("bad allow parse: %+v", c.Expose.Allow)
	}
	if len(c.Connect.Forward) != 1 {
		t.Errorf("bad forward parse: %+v", c.Connect.Forward)
	}
}

func TestExampleIsValidJSON(t *testing.T) {
	var c Config
	if err := json.Unmarshal([]byte(Example()), &c); err != nil {
		t.Fatalf("Example() is not valid JSON: %v", err)
	}
	if c.Server.HTTP == "" || c.Expose.Code == "" || c.Connect.Server == "" {
		t.Errorf("example should populate all sections: %+v", c)
	}
}

// A setting in the wrong section is the config mistake that costs the most
// time, because encoding/json accepts the file and silently drops the line.
func TestUnknownKeys(t *testing.T) {
	data := []byte(`{
	  "expose": {"enabled": true, "socks": ["1080"], "allow": ["127.0.0.1:22"]},
	  "connect": {"forward": ["122:127.0.0.1:22"]},
	  "server": {"admin": {"username": "a", "sess_hours": 3}},
	  "tunnel": {}
	}`)
	got := unknownKeys(data)
	want := []string{"expose.socks", "server.admin.sess_hours", "tunnel"}
	if len(got) != len(want) {
		t.Fatalf("unknownKeys() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("unknownKeys()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// Every key a valid config uses must be recognized, or the warning becomes
// noise that operators learn to ignore.
func TestUnknownKeysAcceptsTheExample(t *testing.T) {
	if got := unknownKeys([]byte(Example())); len(got) != 0 {
		t.Errorf("the example config reports unknown keys: %v", got)
	}
}
