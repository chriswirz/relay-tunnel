package main

import (
	"os"
	"path/filepath"
	"testing"
)

// mkfiles creates a tree of files (paths use "/", content is the path) under root.
func mkfiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// manifestPaths returns just the sorted paths from a manifest.
func manifestPaths(t *testing.T, root string, useGitignore bool) []string {
	t.Helper()
	m, err := buildManifest(root, useGitignore)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, e := range m {
		paths = append(paths, e.Path)
	}
	return paths
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestGitignoreBasic(t *testing.T) {
	root := t.TempDir()
	mkfiles(t, root, map[string]string{
		".gitignore":                "*.log\nbuild/\n/dist\nnode_modules/\n",
		"keep.txt":                  "x",
		"app.log":                   "x",
		"build/out.bin":             "x",
		"dist/bundle.js":            "x",
		"src/main.go":               "x",
		"src/app.log":               "x", // non-anchored *.log matches at any depth
		"node_modules/lib/index.js": "x",
	})
	got := manifestPaths(t, root, true)
	want := []string{".gitignore", "keep.txt", "src/main.go"}
	if !eq(got, want) {
		t.Errorf("with gitignore:\n got  %v\n want %v", got, want)
	}

	// Without gitignore, everything (except .git) is included.
	all := manifestPaths(t, root, false)
	if len(all) != 8 {
		t.Errorf("without gitignore expected 8 files, got %d: %v", len(all), all)
	}
}

func TestGitignoreNegation(t *testing.T) {
	root := t.TempDir()
	mkfiles(t, root, map[string]string{
		".gitignore":    "*.log\n!important.log\n",
		"a.log":         "x",
		"important.log": "x",
		"b.txt":         "x",
	})
	got := manifestPaths(t, root, true)
	want := []string{".gitignore", "b.txt", "important.log"}
	if !eq(got, want) {
		t.Errorf("negation:\n got  %v\n want %v", got, want)
	}
}

func TestGitignoreNested(t *testing.T) {
	root := t.TempDir()
	mkfiles(t, root, map[string]string{
		".gitignore":     "*.tmp\n",
		"a.tmp":          "x",
		"keep.txt":       "x",
		"sub/.gitignore": "!keep.tmp\nsecret.txt\n",
		"sub/keep.tmp":   "x", // re-included by nested negation
		"sub/other.tmp":  "x", // still ignored by root rule
		"sub/secret.txt": "x", // ignored by nested rule
		"sub/normal.txt": "x",
	})
	got := manifestPaths(t, root, true)
	want := []string{".gitignore", "keep.txt", "sub/.gitignore", "sub/keep.tmp", "sub/normal.txt"}
	if !eq(got, want) {
		t.Errorf("nested:\n got  %v\n want %v", got, want)
	}
}

func TestGitignoreSkipsDotGit(t *testing.T) {
	root := t.TempDir()
	mkfiles(t, root, map[string]string{
		"file.txt":           "x",
		".git/config":        "x",
		".git/objects/ab/cd": "x",
	})
	// .git is skipped whether or not gitignore is enabled.
	for _, gi := range []bool{true, false} {
		got := manifestPaths(t, root, gi)
		if !eq(got, []string{"file.txt"}) {
			t.Errorf("gitignore=%v: .git not skipped, got %v", gi, got)
		}
	}
}

func TestGitignoreAnchored(t *testing.T) {
	root := t.TempDir()
	mkfiles(t, root, map[string]string{
		".gitignore": "/dist\n",
		"dist/x":     "x", // anchored: ignored at root
		"sub/dist/y": "x", // not at root: kept
	})
	got := manifestPaths(t, root, true)
	want := []string{".gitignore", "sub/dist/y"}
	if !eq(got, want) {
		t.Errorf("anchored:\n got  %v\n want %v", got, want)
	}
}
