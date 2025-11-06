package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPlanSyncCopyAndDelete(t *testing.T) {
	src := []Entry{
		{Path: "a.txt", Size: 10, Mtime: 100},
		{Path: "dir/b.txt", Size: 20, Mtime: 200},
		{Path: "same.txt", Size: 5, Mtime: 50},
	}
	dst := []Entry{
		{Path: "same.txt", Size: 5, Mtime: 50}, // identical -> no copy
		{Path: "a.txt", Size: 10, Mtime: 999},  // mtime differs -> copy
		{Path: "stale.txt", Size: 1, Mtime: 1}, // only in dst -> delete (with del)
	}

	// Without --delete: only copies for missing/differing files.
	got := planSync(src, dst, false)
	want := []Action{
		{Op: "copy", Path: "a.txt"},
		{Op: "copy", Path: "dir/b.txt"},
	}
	if !equalActions(got, want) {
		t.Errorf("no-delete plan = %+v, want %+v", got, want)
	}

	// With --delete: copies first (sorted), then deletes (sorted).
	got = planSync(src, dst, true)
	want = []Action{
		{Op: "copy", Path: "a.txt"},
		{Op: "copy", Path: "dir/b.txt"},
		{Op: "delete", Path: "stale.txt"},
	}
	if !equalActions(got, want) {
		t.Errorf("delete plan = %+v, want %+v", got, want)
	}
}

func TestPlanSyncSizeChange(t *testing.T) {
	src := []Entry{{Path: "x", Size: 2, Mtime: 5}}
	dst := []Entry{{Path: "x", Size: 3, Mtime: 5}} // same mtime, different size
	got := planSync(src, dst, false)
	if len(got) != 1 || got[0].Op != "copy" {
		t.Errorf("size change should trigger copy, got %+v", got)
	}
}

func TestBuildManifest(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	os.WriteFile(filepath.Join(root, "top.txt"), []byte("hello"), 0o644)
	os.WriteFile(filepath.Join(root, "sub", "nested.txt"), []byte("hi"), 0o644)

	m, err := buildManifest(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 {
		t.Fatalf("expected 2 files, got %d: %+v", len(m), m)
	}
	// Sorted, forward-slash relative paths.
	if m[0].Path != "sub/nested.txt" || m[1].Path != "top.txt" {
		t.Errorf("unexpected manifest paths: %+v", m)
	}
	if m[1].Size != 5 {
		t.Errorf("expected top.txt size 5, got %d", m[1].Size)
	}
}

func TestConfine(t *testing.T) {
	root := t.TempDir()
	if _, ok := confine(root, "a/b.txt"); !ok {
		t.Error("normal relative path should be allowed")
	}
	if _, ok := confine(root, "../escape"); ok {
		t.Error("path escaping root must be denied")
	}
	if _, ok := confine(root, "../../etc/passwd"); ok {
		t.Error("deep escape must be denied")
	}
}

func equalActions(a, b []Action) bool {
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
