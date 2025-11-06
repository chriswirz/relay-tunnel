// Package webui serves the administrative web interface: the statically
// exported Next.js application in web/, embedded into the binary so a server
// needs nothing on disk beyond the binary itself.
//
// The export is a build artifact. `npm run build` in web/ writes it here, into
// out/, which is gitignored apart from a .gitkeep - and that .gitkeep is what
// lets the go:embed below resolve, and so this package compile, in a checkout
// where the frontend has never been built. A binary built that way runs with no
// web interface and says so; the API is unaffected.
package webui

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	sig "github.com/chriswirz/relay-tunnel/internal/signal"
)

//go:embed all:out
var dist embed.FS

//go:embed favicon.ico
var favicon []byte

// buildTime stamps the embedded assets for conditional requests. The binary's
// own modification time is the closest thing to a build stamp available at
// runtime, and a zero fallback is fine: these bytes only change when the binary
// does.
var buildTime = func() time.Time {
	if exe, err := os.Executable(); err == nil {
		if info, err := os.Stat(exe); err == nil {
			return info.ModTime()
		}
	}
	return time.Time{}
}()

// frontend serves the embedded export with the fallbacks a client-routed
// application needs.
type frontend struct {
	files  fs.FS
	server http.Handler
}

// New returns the embedded web interface, or nil when this binary was built
// without one. A nil result is expected rather than exceptional: it is what a
// `go build` with no `npm run build` before it produces, and the server treats
// it as "serve the API alone".
func New() sig.Frontend {
	sub, err := fs.Sub(dist, "out")
	if err != nil {
		return nil
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil
	}
	return &frontend{files: sub, server: http.FileServer(http.FS(sub))}
}

// ServePage writes one exported page by name, so the server can answer with a
// 404 page rather than bare text.
func (f *frontend) ServePage(w http.ResponseWriter, _ *http.Request, name string, status int) bool {
	body, err := fs.ReadFile(f.files, name+".html")
	if err != nil {
		return false
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
	return true
}

func (f *frontend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	switch {
	// Answered from the binary rather than the export, so the tab has an icon
	// even in a build whose frontend was never exported.
	case clean == "favicon.ico":
		w.Header().Set("Content-Type", "image/x-icon")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		http.ServeContent(w, r, "favicon.ico", buildTime, bytes.NewReader(favicon))
		return
	case clean == "" || clean == "." || clean == "index.html":
		f.ServePage(w, r, "index", http.StatusOK)
		return
	// An exact hit: /_next/static/..., /sessions.html.
	case exists(f.files, clean):
		f.server.ServeHTTP(w, r)
		return
	// The export writes /sessions as sessions.html.
	case exists(f.files, clean+".html"):
		r.URL.Path += ".html"
		f.server.ServeHTTP(w, r)
		return
	}
	if !f.ServePage(w, r, "404", http.StatusNotFound) {
		http.NotFound(w, r)
	}
}

func exists(fsys fs.FS, name string) bool {
	if name == "" {
		return false
	}
	info, err := fs.Stat(fsys, name)
	return err == nil && !info.IsDir()
}
