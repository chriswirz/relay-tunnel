package main

// This file defines the small application protocol that relay-sync speaks over
// the QUIC connection provided by the relay-tunnel module. Each request is one
// QUIC stream: the opener writes a length-prefixed JSON Request, and the server
// replies with a length-prefixed JSON control object (optionally followed by
// raw file bytes for "get").

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Request is the first frame the client writes on every stream.
type Request struct {
	Op        string `json:"op"`                  // "manifest" | "get" | "put" | "delete"
	Path      string `json:"path"`                // path relative to the served root
	Size      int64  `json:"size,omitempty"`      // put: number of bytes that follow
	Mtime     int64  `json:"mtime,omitempty"`     // put: modification time to set (unix seconds)
	Mode      uint32 `json:"mode,omitempty"`      // put: unix permission bits
	Gitignore bool   `json:"gitignore,omitempty"` // manifest: exclude .gitignore'd files
}

// Entry is one file in a directory manifest. Paths are relative to the subtree
// root and use forward slashes. Only regular files are listed.
type Entry struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"` // unix seconds
}

// ManifestReply answers a "manifest" request.
type ManifestReply struct {
	OK      bool    `json:"ok"`
	Err     string  `json:"err,omitempty"`
	Entries []Entry `json:"entries,omitempty"`
}

// GetReply precedes the raw bytes of a "get" response.
type GetReply struct {
	OK   bool   `json:"ok"`
	Err  string `json:"err,omitempty"`
	Size int64  `json:"size,omitempty"`
}

// Reply answers "put" and "delete" requests.
type Reply struct {
	OK  bool   `json:"ok"`
	Err string `json:"err,omitempty"`
}

const maxFrame = 32 << 20 // 32 MiB cap for a JSON control frame

// writeFrame writes a length-prefixed byte frame.
func writeFrame(w io.Writer, b []byte) error {
	var lp [4]byte
	binary.BigEndian.PutUint32(lp[:], uint32(len(b)))
	if _, err := w.Write(lp[:]); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

// readFrame reads a length-prefixed byte frame.
func readFrame(r io.Reader) ([]byte, error) {
	var lp [4]byte
	if _, err := io.ReadFull(r, lp[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(lp[:])
	if n > maxFrame {
		return nil, io.ErrUnexpectedEOF
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func writeJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeFrame(w, b)
}

func readJSON(r io.Reader, v any) error {
	b, err := readFrame(r)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// buildManifest walks root and returns a sorted manifest of its regular files,
// with paths relative to root using forward slashes. When useGitignore is true,
// files matched by .gitignore rules in the tree are excluded. The ".git"
// directory is always skipped.
func buildManifest(root string, useGitignore bool) ([]Entry, error) {
	var entries []Entry
	if err := walkTree(root, "", nil, useGitignore, &entries); err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func walkTree(dirAbs, rel string, inherited []ignoreRule, useGitignore bool, out *[]Entry) error {
	rules := inherited
	if useGitignore {
		local, err := loadIgnoreRules(dirAbs, rel)
		if err != nil {
			return err
		}
		if len(local) > 0 {
			rules = append(append([]ignoreRule(nil), inherited...), local...)
		}
	}
	ents, err := os.ReadDir(dirAbs)
	if err != nil {
		return err
	}
	for _, e := range ents {
		name := e.Name()
		if name == ".git" {
			continue
		}
		childRel := name
		if rel != "" {
			childRel = rel + "/" + name
		}
		isDir := e.IsDir()
		if useGitignore && isIgnored(rules, childRel, isDir) {
			continue
		}
		switch {
		case isDir:
			if err := walkTree(filepath.Join(dirAbs, name), childRel, rules, useGitignore, out); err != nil {
				return err
			}
		case e.Type().IsRegular():
			info, err := e.Info()
			if err != nil {
				return err
			}
			*out = append(*out, Entry{Path: childRel, Size: info.Size(), Mtime: info.ModTime().Unix()})
		}
	}
	return nil
}

// Action is a planned sync step.
type Action struct {
	Op   string // "copy" | "delete"
	Path string // relative path (forward slashes)
}

// planSync computes the actions to mirror src onto dst (one-way). A file is
// copied when it is missing from dst or differs in size or mtime. When del is
// true, files present in dst but not src are deleted. The result is
// deterministically ordered: copies (by path) then deletes (by path).
func planSync(src, dst []Entry, del bool) []Action {
	dstByPath := make(map[string]Entry, len(dst))
	for _, e := range dst {
		dstByPath[e.Path] = e
	}
	srcByPath := make(map[string]struct{}, len(src))

	var copies, deletes []Action
	for _, s := range src {
		srcByPath[s.Path] = struct{}{}
		d, ok := dstByPath[s.Path]
		if !ok || d.Size != s.Size || d.Mtime != s.Mtime {
			copies = append(copies, Action{Op: "copy", Path: s.Path})
		}
	}
	if del {
		for _, d := range dst {
			if _, ok := srcByPath[d.Path]; !ok {
				deletes = append(deletes, Action{Op: "delete", Path: d.Path})
			}
		}
	}
	sort.Slice(copies, func(i, j int) bool { return copies[i].Path < copies[j].Path })
	sort.Slice(deletes, func(i, j int) bool { return deletes[i].Path < deletes[j].Path })
	return append(copies, deletes...)
}

// confine resolves rel against root and ensures the result stays within root.
// It returns the cleaned absolute path and whether it is allowed.
func confine(root, rel string) (string, bool) {
	clean := filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	abs, err := filepath.Abs(clean)
	if err != nil {
		return "", false
	}
	if abs == absRoot {
		return abs, true
	}
	if strings.HasPrefix(abs, absRoot+string(os.PathSeparator)) {
		return abs, true
	}
	return "", false
}
