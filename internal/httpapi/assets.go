package httpapi

import (
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// handleDisplay serves the TV page. It is deliberately not cached: when the
// page reloads at 4am, or an admin sends "reload", we want the current build.
func (s *Server) handleDisplay(w http.ResponseWriter, r *http.Request) {
	f, err := s.displayFS.Open("index.html")
	if err != nil {
		http.Error(w, "display page missing from this build", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.Copy(w, f)
}

// spaHandler serves the built portal, falling back to index.html for client
// routes like /admin so a deep link or a refresh does not 404.
func (s *Server) spaHandler() http.Handler {
	files := http.FileServer(http.FS(s.portalFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// An unrouted API or media path is a mistake, not a client route.
		// Falling through to index.html would hand the caller HTML and turn a
		// simple 404 into a JSON parse error somewhere further away.
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/media/") {
			writeError(w, http.StatusNotFound, "no such endpoint")
			return
		}

		clean := path.Clean(strings.TrimPrefix(r.URL.Path, "/"))
		if clean == "." {
			clean = "index.html"
		}
		if f, err := s.portalFS.Open(clean); err == nil {
			info, statErr := f.Stat()
			f.Close()
			if statErr == nil && !info.IsDir() {
				// Vite fingerprints everything under /assets, so those are
				// immutable; index.html must never be.
				if strings.HasPrefix(clean, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					w.Header().Set("Cache-Control", "no-cache")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		s.serveIndex(w)
	})
}

func (s *Server) serveIndex(w http.ResponseWriter) {
	f, err := s.portalFS.Open("index.html")
	if err != nil {
		http.Error(w, "portal not built — run `make portal` before building the server", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = io.Copy(w, f)
}

// MustSub narrows an embedded FS to a subdirectory, panicking on a broken
// build rather than failing mysteriously at request time.
func MustSub(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic("embed: " + dir + ": " + err.Error())
	}
	return sub
}
