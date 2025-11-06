package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// SaveAdmin writes a changed administrator username and/or password hash into
// the server section of the config file at path, creating the file if it is not
// there yet. An empty string for either field leaves that one alone.
//
// The file is read as a generic map rather than into Config, so anything this
// build does not know about - a field added by a newer version, a comment-like
// key someone added by hand - survives the round trip. Only the two keys being
// changed are touched.
//
// The write is atomic: a temporary file in the same directory is renamed over
// the original, so a crash mid-write cannot leave a config that will not parse
// and lock the administrator out.
func SaveAdmin(path, username, passwordHash string) error {
	if username == "" && passwordHash == "" {
		return errors.New("nothing to save")
	}
	if path == "" {
		return errors.New("no config file path: start the server with --config to persist account changes")
	}

	doc := map[string]any{}
	switch data, err := os.ReadFile(path); {
	case err == nil:
		if len(data) > 0 {
			if err := json.Unmarshal(data, &doc); err != nil {
				return fmt.Errorf("parse %s: %w", path, err)
			}
		}
	case errors.Is(err, fs.ErrNotExist):
		// A server started without a config file still gets one, so the password
		// chosen in the UI outlives the process.
	default:
		return err
	}

	server, _ := doc["server"].(map[string]any)
	if server == nil {
		server = map[string]any{}
	}
	admin, _ := server["admin"].(map[string]any)
	if admin == nil {
		admin = map[string]any{}
	}
	if username != "" {
		admin["username"] = username
	}
	if passwordHash != "" {
		admin["password_hash"] = passwordHash
	}
	server["admin"] = admin
	doc["server"] = server

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".relay-tunnel-config-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename below has succeeded

	// 0600: this file holds the password hash, and on a fresh install it is the
	// only thing standing between a passer-by and the admin interface.
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, os.ErrInvalid) {
		// Chmod is not supported on every platform (notably Windows sets only
		// the read-only bit); a failure here is not worth losing the write over.
		_ = err
	}
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
