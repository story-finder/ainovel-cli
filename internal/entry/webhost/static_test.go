package webhost

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRootServesStandaloneWebHostUI(t *testing.T) {
	app := newServer(newFakeRuntime(), 8)
	t.Cleanup(app.Close)

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}

	content := response.Body.String()
	for _, want := range []string{`id="start-form"`, `src="/app.js"`} {
		if !strings.Contains(content, want) {
			t.Fatalf("GET / body does not contain %q: %s", want, content)
		}
	}
}

func TestStaticAssetsUseRootRelativeRoutes(t *testing.T) {
	app := newServer(newFakeRuntime(), 8)
	t.Cleanup(app.Close)

	for _, path := range []string{"/app.js", "/app.css"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want %d", path, response.Code, http.StatusOK)
		}
		if response.Body.Len() == 0 {
			t.Fatalf("GET %s returned an empty body", path)
		}
	}
}

func TestStaticAssetsUseOnlyRelativeLocalPaths(t *testing.T) {
	app, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded web/app.js: %v", err)
	}

	content := string(app)
	for _, forbidden := range []string{"http://", "https://"} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("web/app.js contains forbidden external path %q", forbidden)
		}
	}
}
