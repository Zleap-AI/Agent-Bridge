package main

import (
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

// The release build can replace this small embedded filesystem with the
// compiled Local Console assets without changing routing or API composition.
//
//go:embed html/index.html
var embeddedLocalConsole embed.FS

func newLocalConsoleHandler() http.Handler {
	assets, err := fs.Sub(embeddedLocalConsole, "html")
	if err != nil {
		return http.NotFoundHandler()
	}
	return newSPAHandler(assets, "index.html")
}

func newSPAHandler(assets fs.FS, index string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" || name == "." {
			name = index
		}
		data, err := fs.ReadFile(assets, name)
		if err != nil {
			name = index
			data, err = fs.ReadFile(assets, name)
		}
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		if name == index {
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		_, _ = w.Write(data)
	})
}
