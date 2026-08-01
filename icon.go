package main

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"net/http"
	"time"
)

// appIcon is the application icon, embedded so the binary stays a single file
// with nothing to install alongside it. The same image is the Windows
// executable icon (through the .syso resources) and what the HTTP transport
// answers /favicon.ico with, so the server looks like itself wherever it shows
// up: a browser tab, a bookmark, an MCP client that renders one.
//
//go:embed appicon.ico
var appIcon []byte

// appIconETag identifies this icon's bytes, so a browser that has it already
// gets a 304 rather than the whole file. The icon changes only when the binary
// is rebuilt, which makes a content hash exactly the right validator.
var appIconETag = func() string {
	sum := sha256.Sum256(appIcon)
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}()

// faviconPath is the well-known location a browser asks for unprompted.
const faviconPath = "/favicon.ico"

// serveFavicon answers /favicon.ico with the application icon.
//
// It sits outside the bearer-token check on purpose. A browser requests a
// favicon on its own, with no way to attach an Authorization header, so
// requiring the token here would mean a broken icon on every authenticated
// server - and an application icon is not a secret in the first place.
func (t *HTTPTransport) serveFavicon(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "image/x-icon")
	w.Header().Set("ETag", appIconETag)
	// A day is long enough to stop the repeat requests a browser makes across
	// tabs, and short enough that a rebuilt icon appears without a hard reload.
	w.Header().Set("Cache-Control", "public, max-age=86400")

	// A zero modification time keeps ServeContent from emitting Last-Modified,
	// which would otherwise vary between builds of the same bytes; the ETag is
	// the validator that means something here.
	http.ServeContent(w, r, "favicon.ico", time.Time{}, bytes.NewReader(appIcon))
}
