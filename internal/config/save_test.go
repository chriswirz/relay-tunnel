package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestSaveAdminPreservesTheRestOfTheFile is the point of writing through a
// generic map: an account change is not an excuse to rewrite someone's config.
func TestSaveAdminPreservesTheRestOfTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := `{
  "server": {"http": ":7000", "udp": ":7001", "admin": {"session_hours": 24}},
  "expose": {"code": "quiet-harbor", "allow_dial": true},
  "something_this_build_does_not_know": {"keep": "me"}
}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := SaveAdmin(path, "crwirz", "pbkdf2-sha256$1$a$b"); err != nil {
		t.Fatal(err)
	}

	var doc map[string]any
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("the saved config does not parse: %v\n%s", err, data)
	}

	if _, ok := doc["something_this_build_does_not_know"]; !ok {
		t.Error("an unknown top level section was dropped")
	}
	if expose, ok := doc["expose"].(map[string]any); !ok || expose["code"] != "quiet-harbor" {
		t.Errorf("the expose section was disturbed: %v", doc["expose"])
	}
	server, _ := doc["server"].(map[string]any)
	if server["http"] != ":7000" || server["udp"] != ":7001" {
		t.Errorf("other server settings were disturbed: %v", server)
	}
	admin, _ := server["admin"].(map[string]any)
	if admin["username"] != "crwirz" || admin["password_hash"] != "pbkdf2-sha256$1$a$b" {
		t.Errorf("the account was not saved: %v", admin)
	}
	if admin["session_hours"] != float64(24) {
		t.Errorf("session_hours was disturbed: %v", admin["session_hours"])
	}

	// A second call touching only the password leaves the name alone.
	if err := SaveAdmin(path, "", "pbkdf2-sha256$2$c$d"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Admin.Username != "crwirz" || cfg.Server.Admin.PasswordHash != "pbkdf2-sha256$2$c$d" {
		t.Errorf("second save = %+v", cfg.Server.Admin)
	}
}

// TestSaveAdminCreatesAMissingFile covers the common first boot: a server
// started with no config at all, whose administrator picks a password in the
// browser and expects it to survive a restart.
func TestSaveAdminCreatesAMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := SaveAdmin(path, "", "pbkdf2-sha256$1$a$b"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Admin.PasswordHash != "pbkdf2-sha256$1$a$b" {
		t.Fatalf("saved config = %+v", cfg.Server.Admin)
	}
}

func TestSaveAdminRejectsUnusableCalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := SaveAdmin(path, "", ""); err == nil {
		t.Error("saving nothing should be an error")
	}
	if err := SaveAdmin("", "crwirz", ""); err == nil {
		t.Error("saving with no path should be an error")
	}

	// A config that does not parse is left alone rather than overwritten: it is
	// more likely to be a typo someone is midway through fixing than junk.
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveAdmin(path, "crwirz", ""); err == nil {
		t.Error("saving over an unparseable config should be an error")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{not json" {
		t.Errorf("the unparseable config was overwritten with %q", data)
	}
}
