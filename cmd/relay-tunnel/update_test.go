package main

import (
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUpdateAssetName(t *testing.T) {
	got := updateAssetName()
	want := fmt.Sprintf("relay-tunnel-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		want += ".exe"
	}
	if got != want {
		t.Errorf("updateAssetName() = %q, want %q", got, want)
	}
}

// The release sums the examples and the .deb/.rpm packages alongside the CLI,
// so a prefix match would pick the wrong line; only the exact name may match.
func TestChecksumFor(t *testing.T) {
	sums := "aaaa  relay-tunnel-linux-amd64\n" +
		"bbbb *relay-tunnel-windows-amd64.exe\n" +
		"\n" +
		"CCCC  relay-tunnel_0.1.0007_amd64.deb\n" +
		"dddd  examples/relay-sync-linux-amd64\n"
	cases := map[string]string{
		"relay-tunnel-linux-amd64":        "aaaa",
		"relay-tunnel-windows-amd64.exe":  "bbbb",
		"relay-tunnel_0.1.0007_amd64.deb": "cccc",
		"examples/relay-sync-linux-amd64": "dddd",
	}
	for asset, want := range cases {
		got, err := checksumFor(sums, asset)
		if err != nil {
			t.Fatalf("checksumFor(%q): %v", asset, err)
		}
		if got != want {
			t.Errorf("checksumFor(%q) = %q, want %q", asset, got, want)
		}
	}
	if _, err := checksumFor(sums, "relay-tunnel-plan9-386"); err == nil {
		t.Error("checksumFor accepted an asset the release does not carry")
	}
}

// The rolling release is tagged after the branch while its name carries the
// version, so downloading follows the tag and "am I current?" follows the name.
func TestReleaseVersion(t *testing.T) {
	cases := []struct {
		name string
		rel  release
		want string
	}{
		{"rolling", release{TagName: "main", Name: "v0.1.0042 (abc1234)"}, "v0.1.0042"},
		{"tagged", release{TagName: "v0.2.0", Name: "v0.2.0"}, "v0.2.0"},
		{"unnamed", release{TagName: "v0.2.0"}, "v0.2.0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.rel.version(); got != c.want {
				t.Errorf("version() = %q, want %q", got, c.want)
			}
		})
	}
}

// downloadAndReplace must install only what the release's own SHA256SUMS
// vouches for, since it is overwriting the binary that is running.
func TestDownloadAndReplace(t *testing.T) {
	payload := []byte("#!/bin/sh\necho new\n")
	sum := hex.EncodeToString(sha256Sum(payload))
	asset := updateAssetName()

	serve := func(sums string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/SHA256SUMS"):
				io.WriteString(w, sums)
			case strings.HasSuffix(r.URL.Path, "/"+asset):
				w.Write(payload)
			default:
				http.NotFound(w, r)
			}
		}))
	}

	install := func(t *testing.T, sums string) (string, error) {
		t.Helper()
		srv := serve(sums)
		defer srv.Close()
		exe := filepath.Join(t.TempDir(), "relay-tunnel")
		if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
			t.Fatal(err)
		}
		err := downloadAndReplace(srv.Client(), srv.URL, exe, "v0.1.0042", io.Discard)
		got, readErr := os.ReadFile(exe)
		if readErr != nil {
			t.Fatalf("the binary is gone after the update: %v", readErr)
		}
		return string(got), err
	}

	t.Run("installs a matching checksum", func(t *testing.T) {
		got, err := install(t, sum+"  "+asset+"\n")
		if err != nil {
			t.Fatalf("downloadAndReplace: %v", err)
		}
		if got != string(payload) {
			t.Errorf("binary = %q, want the downloaded payload", got)
		}
	})

	t.Run("refuses a mismatched checksum", func(t *testing.T) {
		got, err := install(t, strings.Repeat("0", 64)+"  "+asset+"\n")
		if err == nil {
			t.Fatal("downloadAndReplace accepted a payload the checksum does not cover")
		}
		if got != "old" {
			t.Errorf("binary = %q, want the original left in place", got)
		}
	})

	t.Run("refuses a release without this platform", func(t *testing.T) {
		got, err := install(t, sum+"  relay-tunnel-plan9-386\n")
		if err == nil {
			t.Fatal("downloadAndReplace accepted a release carrying no build for this platform")
		}
		if got != "old" {
			t.Errorf("binary = %q, want the original left in place", got)
		}
	})
}

// A staged file left behind would accumulate next to the binary on every run.
func TestReplaceExecutableLeavesNoStagingFile(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "relay-tunnel")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceExecutable(exe, []byte("new")); err != nil {
		t.Fatalf("replaceExecutable: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "relay-tunnel" {
			t.Errorf("left %q behind next to the binary", e.Name())
		}
	}
}
