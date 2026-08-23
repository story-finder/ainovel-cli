package webhost

import (
	"bytes"
	"embed"
	"net/http"
	"time"
)

//go:embed web/*
var webFS embed.FS

func (s *server) handleStatic(w http.ResponseWriter, r *http.Request) {
	path := ""
	switch r.URL.Path {
	case "/":
		path = "web/index.html"
	case "/app.js":
		path = "web/app.js"
	case "/app.css":
		path = "web/app.css"
	case "/markdown.js":
		path = "web/markdown.js"
	default:
		s.handleNotFound(w, r)
		return
	}

	contents, err := webFS.ReadFile(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "embedded web asset is unavailable")
		return
	}
	http.ServeContent(w, r, path, time.Time{}, bytes.NewReader(contents))
}
