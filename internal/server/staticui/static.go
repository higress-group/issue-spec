// Package staticui serves the checked-in production SPA build. Keeping the
// generated Vite output in this package makes a fresh Go checkout buildable
// without Node while the release target can still reproduce and verify it.
package staticui

import (
	"embed"
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"strings"
)

//go:embed dist
var production embed.FS

type Options struct {
	DevelopmentDirectory string
	Production           bool
}

type Handler struct {
	assets fs.FS
	info   map[string]Asset
}

func New(options Options) (*Handler, error) {
	if options.Production && strings.TrimSpace(options.DevelopmentDirectory) != "" {
		return nil, errors.New("static ui: external asset directory is forbidden in production")
	}
	assets, err := fs.Sub(production, "dist")
	if err != nil {
		return nil, err
	}
	info := manifest
	if directory := strings.TrimSpace(options.DevelopmentDirectory); directory != "" {
		root, err := os.OpenRoot(directory)
		if err != nil {
			return nil, err
		}
		assets = root.FS()
		info = nil
	}
	if _, err := fs.Stat(assets, "index.html"); err != nil {
		return nil, errors.New("static ui: index.html is missing")
	}
	return &Handler{assets: assets, info: info}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}
	if reservedPath(r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "." || name == "" {
		name = "index.html"
	}
	content, err := fs.ReadFile(h.assets, name)
	if err != nil {
		if path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
		name = "index.html"
		content, err = fs.ReadFile(h.assets, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
	}
	h.write(w, r, name, content)
}

func (h *Handler) write(w http.ResponseWriter, r *http.Request, name string, content []byte) {
	asset, known := h.info[name]
	contentType := asset.ContentType
	if contentType == "" {
		contentType = mime.TypeByExtension(path.Ext(name))
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	if name == "index.html" {
		w.Header().Set("Cache-Control", "no-store")
	} else if known && asset.Immutable {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("ETag", `"`+asset.SHA256+`"`)
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.Header().Set("Content-Length", strconvItoa(len(content)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(content)
	}
}

func reservedPath(value string) bool {
	clean := path.Clean("/" + value)
	return clean == "/user" || clean == "/livez" || clean == "/readyz" ||
		clean == "/metrics" || strings.HasPrefix(clean, "/api/") || clean == "/api" ||
		strings.HasPrefix(clean, "/repos/") || clean == "/repos" ||
		strings.HasPrefix(clean, "/notifications")
}

func strconvItoa(value int) string {
	if value == 0 {
		return "0"
	}
	var buffer [32]byte
	position := len(buffer)
	for value > 0 {
		position--
		buffer[position] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[position:])
}
