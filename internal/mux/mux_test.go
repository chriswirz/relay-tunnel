package mux

import (
	"bytes"
	"testing"
)

func TestHeaderRoundTrip(t *testing.T) {
	cases := []OpenMsg{
		{Kind: KindTCP, Target: "example.com:443"},
		{Kind: KindFile, File: &FileHeader{Op: "put", Path: "a/b.txt", Size: 1234, Mode: 0o644}},
		{Kind: KindExec, Exec: &ExecHeader{Args: []string{"ls", "-la", "/tmp"}}},
	}
	for _, want := range cases {
		var buf bytes.Buffer
		if err := WriteHeader(&buf, want); err != nil {
			t.Fatalf("WriteHeader: %v", err)
		}
		got, err := ReadHeader(&buf)
		if err != nil {
			t.Fatalf("ReadHeader: %v", err)
		}
		if got.Kind != want.Kind || got.Target != want.Target {
			t.Errorf("kind/target mismatch: got %+v want %+v", got, want)
		}
		if (got.File == nil) != (want.File == nil) || (got.Exec == nil) != (want.Exec == nil) {
			t.Errorf("nested header presence mismatch: got %+v want %+v", got, want)
		}
		if want.File != nil && (got.File.Op != want.File.Op || got.File.Path != want.File.Path || got.File.Size != want.File.Size) {
			t.Errorf("file header mismatch: got %+v want %+v", got.File, want.File)
		}
		if want.Exec != nil && len(got.Exec.Args) != len(want.Exec.Args) {
			t.Errorf("exec args mismatch: got %+v want %+v", got.Exec, want.Exec)
		}
	}
}

func TestReadHeaderRejectsOversize(t *testing.T) {
	var buf bytes.Buffer
	// length prefix claiming > maxHeader
	buf.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF})
	if _, err := ReadHeader(&buf); err == nil {
		t.Fatal("expected error for oversize header")
	}
}
