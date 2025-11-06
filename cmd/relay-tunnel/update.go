package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Self-update against the project's own releases. The rolling release carries
// every platform's binary under a stable name alongside a SHA256SUMS file
// covering all of them, which is what makes replacing this binary in place a
// download and a checksum rather than a package manager.
const (
	appName            = "relay-tunnel"
	updateRepo         = "chriswirz/relay-tunnel"
	updateAPI          = "https://api.github.com/repos/" + updateRepo + "/releases"
	updateDownloadBase = "https://github.com/" + updateRepo + "/releases/download"
)

// updateAssetName is the release asset for the platform this binary was built
// for, which is the only one it can replace itself with. It must match the
// names build.sh writes into dist/.
func updateAssetName() string {
	name := fmt.Sprintf("%s-%s-%s", appName, runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// selfUpdate downloads the named release - or the latest, when tag is empty -
// verifies it against that release's SHA256SUMS, and replaces the running
// binary with it. Progress goes to out.
func selfUpdate(out io.Writer, tag string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not find the running binary: %w", err)
	}
	// A symlinked install (~/.local/bin pointing elsewhere, a Homebrew shim)
	// has to be replaced where the binary really lives.
	if resolved, linkErr := filepath.EvalSymlinks(exe); linkErr == nil {
		exe = resolved
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	label := tag
	if tag == "" {
		rel, err := latestRelease(client)
		if err != nil {
			return err
		}
		// The rolling release is tagged after the branch while its name carries
		// the version, so the tag is where to download from and the name is
		// what to compare against. See .github/workflows/ci.yml.
		tag, label = rel.TagName, rel.version()
		if label == version {
			fmt.Fprintf(out, "%s %s is already the latest release.\n", appName, version)
			return nil
		}
	}

	base := fmt.Sprintf("%s/%s", updateDownloadBase, tag)
	return downloadAndReplace(client, base, exe, label, out)
}

// downloadAndReplace fetches this platform's asset from one release's download
// base, checks it against that release's SHA256SUMS, and installs it. Nothing
// is written unless the checksum matches.
func downloadAndReplace(client *http.Client, base, exe, label string, out io.Writer) error {
	asset := updateAssetName()
	fmt.Fprintf(out, "updating %s from %s to %s (%s)\n", exe, version, label, asset)

	sums, err := fetchURL(client, base+"/SHA256SUMS")
	if err != nil {
		return fmt.Errorf("could not download SHA256SUMS for %s: %w", label, err)
	}
	want, err := checksumFor(string(sums), asset)
	if err != nil {
		return err
	}
	binary, err := fetchURL(client, base+"/"+asset)
	if err != nil {
		return fmt.Errorf("could not download %s: %w", asset, err)
	}
	got := hex.EncodeToString(sha256Sum(binary))
	if got != want {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s - nothing was written", asset, got, want)
	}

	if err := replaceExecutable(exe, binary); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s is now %s.\n", exe, label)
	fmt.Fprintf(out, "Restart any running server or host to pick it up; a live tunnel keeps the old binary until it ends.\n")
	return nil
}

// release is the part of a GitHub release this needs: where to download from,
// and what to call the version once it is installed.
type release struct {
	TagName string `json:"tag_name"`
	Name    string `json:"name"`
}

// version is the release's version string. The rolling release is tagged after
// the branch and names itself "v0.1.0042 (abc1234)", so the first field of the
// name is the version; a v* tag names itself after the tag and both agree.
func (r release) version() string {
	if f := strings.Fields(r.Name); len(f) > 0 {
		return f[0]
	}
	return r.TagName
}

// latestRelease asks GitHub which release is current. The API is
// unauthenticated here, so a rate-limited answer is reported as such rather
// than as a mysterious failure.
func latestRelease(client *http.Client) (release, error) {
	body, err := fetchURL(client, updateAPI+"/latest")
	if err != nil {
		return release{}, fmt.Errorf("could not ask GitHub for the latest release: %w", err)
	}
	var rel release
	if err := json.Unmarshal(body, &rel); err != nil {
		return release{}, fmt.Errorf("could not read the latest release: %w", err)
	}
	if rel.TagName == "" {
		return release{}, errors.New("the latest release has no tag")
	}
	return rel, nil
}

// checksumFor picks one asset's line out of a SHA256SUMS file, which lists
// "<hex>  <name>" one artifact per line. The release sums packages and the
// bundled examples too, so the name has to match exactly.
func checksumFor(sums, asset string) (string, error) {
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 {
			continue
		}
		if strings.TrimPrefix(fields[1], "*") == asset {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("SHA256SUMS does not list %s; this release may not carry a build for %s/%s",
		asset, runtime.GOOS, runtime.GOARCH)
}

func sha256Sum(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

func fetchURL(client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", appName+"/"+version)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("GitHub answered %s (rate limited); try again later or pass --update-version to skip the API",
			resp.Status)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 256<<20))
}

// replaceExecutable writes the new binary next to the old one and moves it into
// place. The staging file shares a directory with the target so the move is a
// rename within one filesystem, and a failure part-way leaves the old binary
// working.
func replaceExecutable(exe string, binary []byte) error {
	dir := filepath.Dir(exe)
	staged, err := os.CreateTemp(dir, "."+filepath.Base(exe)+".new-*")
	if err != nil {
		return updateWriteError(dir, err)
	}
	stagedName := staged.Name()
	defer os.Remove(stagedName) // no-op once the rename has taken it away

	if _, err := staged.Write(binary); err != nil {
		staged.Close()
		return fmt.Errorf("could not write %s: %w", stagedName, err)
	}
	if err := staged.Close(); err != nil {
		return fmt.Errorf("could not write %s: %w", stagedName, err)
	}
	// CreateTemp makes the file 0600; the installed binary has to be runnable.
	mode := os.FileMode(0o755)
	if info, statErr := os.Stat(exe); statErr == nil {
		mode = info.Mode().Perm() | 0o111
	}
	if err := os.Chmod(stagedName, mode); err != nil {
		return fmt.Errorf("could not set the mode on %s: %w", stagedName, err)
	}

	// Windows will not overwrite a running image, but it will rename it: the
	// old binary is moved aside and deleted on the next update if the OS still
	// holds it open. On unix the rename alone is atomic.
	backup := exe + ".old"
	os.Remove(backup)
	if err := os.Rename(exe, backup); err != nil {
		return updateWriteError(dir, err)
	}
	if err := os.Rename(stagedName, exe); err != nil {
		// Put the working binary back rather than leaving nothing installed.
		os.Rename(backup, exe)
		return updateWriteError(dir, err)
	}
	// Windows keeps the old image open for as long as this process runs, so the
	// removal fails there and the .old file is cleared by the next update
	// instead. Either way the update itself has stood, and a leftover file is
	// not worth reporting as a failure.
	os.Remove(backup)
	return nil
}

// updateWriteError turns a permission failure into the advice that goes with
// it, since a system-wide install is the common case and needs elevation.
func updateWriteError(dir string, err error) error {
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("cannot write to %s: %w\nrun the update with the privileges that directory needs (sudo, or an elevated shell)", dir, err)
	}
	return fmt.Errorf("cannot write to %s: %w", dir, err)
}
