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
		"MAX_INLINE_DEPTH",
		"MAX_INLINE_SCAN",
		"MAX_CODE_SPAN_SCAN",
		"MAX_CODE_SPAN_MARKER",
		"function isHorizontalRule",
		"function renderTable",
		"render: renderMarkdown",
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
		`lastEventID = null;`,
		`resetChatTranscript();`,
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

func TestAppRendersUISnapshotDiagnosticsAndRuntimeReplay(t *testing.T) {
	content := embeddedAppJS(t)
	if strings.Index(content, `const welcomeMessage =`) > strings.Index(content, `const state =`) {
		t.Fatal("web/app.js uses welcomeMessage before declaring it")
	}
	for _, want := range []string{
		`get(snapshot, "contextStrategy", "ContextStrategy")`,
		`get(snapshot, "contextCompactedCount", "ContextCompactedCount")`,
		`get(snapshot, "missingAssistantUsage", "MissingAssistantUsage")`,
		`get(snapshot, "overallRecentCacheRead", "OverallRecentCacheRead")`,
		`get(snapshot, "rewriteReason", "RewriteReason")`,
		`get(snapshot, "outline", "Outline")`,
		`get(snapshot, "characters", "Characters")`,
		`get(snapshot, "premise", "Premise")`,
		`get(snapshot, "supportingCount", "SupportingCount")`,
		`get(snapshot, "recentSummaries", "RecentSummaries")`,
		`get(snapshot, "layered", "Layered")`,
		`get(snapshot, "inProgressChapter", "InProgressChapter")`,
		`get(snapshot, "cachePerAgent", "CachePerAgent")`,
		`get(snapshot, "cachePerModel", "CachePerModel")`,
		`kind === "ui_event"`,
		`addEventMessage(`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("web/app.js does not contain snapshot/replay assertion %q", want)
		}
	}

	index := embeddedIndexHTML(t)
	for _, want := range []string{
		`id="status-context-strategy"`,
		`id="status-missing-usage"`,
		`id="status-cache-recent"`,
		`id="status-rewrite-reason"`,
		`id="status-novel"`,
		`id="status-outline"`,
		`id="status-characters"`,
		`id="status-premise"`,
		`id="status-supporting"`,
		`id="status-compass"`,
		`id="status-summaries"`,
		`id="status-layered"`,
		`id="status-outline-core"`,
		`id="status-cache-by-agent"`,
		`id="status-cache-by-model"`,
		`id="status-agent-context"`,
	} {
		if !strings.Contains(index, want) {
			t.Fatalf("web/index.html does not contain snapshot field %q", want)
		}
	}
}

func TestAssistantResultsAreNotRenderedAsUserBubbles(t *testing.T) {
	content := embeddedCSS(t)
	for _, want := range []string{
		`.message-result {`,
		`border: 0;`,
		`background: transparent;`,
		`.message-user {`,
		`max-width: min(75%, 42rem);`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("web/app.css does not contain full-width response assertion %q", want)
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

func embeddedIndexHTML(t *testing.T) string {
	t.Helper()
	index, err := webFS.ReadFile("web/index.html")
	if err != nil {
		t.Fatalf("read embedded web/index.html: %v", err)
	}
	return string(index)
}

func embeddedCSS(t *testing.T) string {
	t.Helper()
	css, err := webFS.ReadFile("web/app.css")
	if err != nil {
		t.Fatalf("read embedded web/app.css: %v", err)
	}
	return string(css)
}

func embeddedMarkdownJS(t *testing.T) string {
	t.Helper()
	markdown, err := webFS.ReadFile("web/markdown.js")
	if err != nil {
		t.Fatalf("read embedded web/markdown.js: %v", err)
	}
	return string(markdown)
}
