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

	for _, path := range []string{"/app.js", "/app.css", "/markdown.js"} {
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

func TestMarkdownAssetContainsSanitizedRenderer(t *testing.T) {
	content := embeddedMarkdownJS(t)
	for _, want := range []string{
		"function renderMarkdown",
		"function escapeHTML",
		"(?:javascript|data):",
		"container.innerHTML = renderMarkdown(markdown)",
		"MAX_BLOCKQUOTE_DEPTH",
		"renderInto",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("web/markdown.js does not contain renderer safety assertion %q", want)
		}
	}
}

func TestStaticAssetsUseOnlyRelativeLocalPaths(t *testing.T) {
	content := embeddedAppJS(t)
	for _, forbidden := range []string{"http://", "https://"} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("web/app.js contains forbidden external path %q", forbidden)
		}
	}
}

func TestAppHydratesPendingQuestionsFromStatus(t *testing.T) {
	content := embeddedAppJS(t)
	for _, want := range []string{
		`const pending = field(payload, "pending", "Pending");`,
		`if (pending) {`,
		`renderQuestionFrame(pending);`,
		`clearQuestionFrame();`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("web/app.js does not contain pending-question hydration assertion %q", want)
		}
	}
}

func TestAppIgnoresObsoleteStatusResponses(t *testing.T) {
	content := embeddedAppJS(t)
	for _, want := range []string{
		`let latestStatusRequestID = 0;`,
		`const requestID = ++latestStatusRequestID;`,
		`function renderStatus(payload, requestID, requestRevision) {`,
		`if (requestID !== latestStatusRequestID || requestRevision !== questionRevision) {`,
		`if (requestID === latestStatusRequestID && requestRevision === questionRevision) {`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("web/app.js does not contain status ordering assertion %q", want)
		}
	}
}

func TestAppReconnectsFromLatestEventCursor(t *testing.T) {
	content := embeddedAppJS(t)
	for _, want := range []string{
		`let lastEventID = null;`,
		`event.lastEventId`,
		`/events?after=`,
		`eventSource.addEventListener("heartbeat", rememberEventID);`,
		`eventSource.addEventListener("runtime_replay", rememberEventID);`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("web/app.js does not contain reconnect cursor assertion %q", want)
		}
	}
}

func TestAppMakesCustomAnswersExclusiveForSingleSelect(t *testing.T) {
	content := embeddedAppJS(t)
	for _, want := range []string{
		`if (multiSelect) {`,
		`selected.push(customText);`,
		`selected = [customText];`,
		`notes[questionText] = customText;`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("web/app.js does not contain single-select custom answer assertion %q", want)
		}
	}
}

func embeddedAppJS(t *testing.T) string {
	t.Helper()
	app, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded web/app.js: %v", err)
	}
	return string(app)
}

func embeddedMarkdownJS(t *testing.T) string {
	t.Helper()
	markdown, err := webFS.ReadFile("web/markdown.js")
	if err != nil {
		t.Fatalf("read embedded web/markdown.js: %v", err)
	}
	return string(markdown)
}
