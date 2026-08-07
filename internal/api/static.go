package api

import (
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
)

// StaticHandler serves the built dashboard.
//
// The dashboard is static files. core serves them from the same origin as the
// API so that the browser's same-origin policy does the work, and so that a
// home server with the internet unplugged still has a working interface —
// nothing here is fetched from a CDN.
func StaticHandler(dir string) (http.Handler, error) {
	if _, err := os.Stat(path.Join(dir, "index.html")); err != nil {
		return nil, err
	}
	root := os.DirFS(dir)
	return &staticServer{root: root, files: http.FileServerFS(root)}, nil
}

type staticServer struct {
	root  fs.FS
	files http.Handler
}

func (s *staticServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The API is mounted separately; anything reaching here that looks like an
	// API call is a mistake, and serving index.html for it would turn a typo
	// into a confusing HTML response where JSON was expected.
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": map[string]any{
				"code":        "not_found",
				"message":     "No such endpoint.",
				"recoverable": false,
			},
		})
		return
	}

	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "" || name == "." {
		name = "index.html"
	}

	if info, err := fs.Stat(s.root, name); err == nil && !info.IsDir() {
		// Hashed asset filenames change whenever their content does, so they can
		// be cached indefinitely. index.html cannot: it is what points at the
		// current asset names, and a stale copy would load a version of the
		// dashboard that no longer exists.
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		s.files.ServeHTTP(w, r)
		return
	}

	// Anything else is a dashboard route, which the browser resolves. Serving
	// index.html means a reload on a sub-page works rather than 404ing.
	w.Header().Set("Cache-Control", "no-cache")
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/"
	s.files.ServeHTTP(w, r2)
}
