# AI Novel web chat UI redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rebuild the web host around a Vietnamese TUI-compatible chatbox with full-width Markdown responses, live `UISnapshot` status, and executable `/model`, `/diag`, `/export`, `/import`, `/simulate`, and `/cocreate` commands.

**Architecture:** Keep `Host` as the source of truth. Add a narrow web command adapter around the existing Host/core operations, publish command progress/results through the existing SSE hub, and leave `/status` as the complete snapshot endpoint. Replace the three-form browser layout with a single chat transcript, a slash-command palette, a model picker, and a collapsible TUI-style status panel.

**Tech Stack:** Go 1.25, `net/http`, existing `host`/`diag`/`exp`/`imp`/`sim` packages, embedded static HTML/CSS/JavaScript, Node built-in test runner for pure Markdown helpers.

---

## File map

- Create `internal/entry/webhost/commands.go`: exact web-visible TUI command catalog, slash parser, model catalog DTOs, argument parsing, and command dispatch helpers.
- Create `internal/entry/webhost/commands_test.go`: parser, catalog, model payload, and async command result tests.
- Modify `internal/entry/webhost/server.go`: command request fields including startup mode, `/models`, `action: command`, capability interfaces, and command result publishing.
- Modify `internal/entry/webhost/server_test.go`: status field coverage and command endpoint regression cases.
- Modify `internal/entry/webhost/sse.go` only if a shared SSE payload helper is needed; preserve replay and `Last-Event-ID` behavior.
- Create `internal/entry/webhost/web/markdown.js`: escaped Markdown renderer used by the browser and Node tests.
- Create `internal/entry/webhost/web/markdown.test.js`: safe rendering tests for the supported Markdown subset.
- Modify `internal/entry/webhost/web/index.html`: Vietnamese chat layout, status strip, detail panel, command palette, model picker, question panel, and composer.
- Modify `internal/entry/webhost/web/app.css`: reuse the existing dark/accent palette for chat, status, Markdown, logs, panels, and responsive layout.
- Modify `internal/entry/webhost/web/app.js`: transcript state, SSE message assembly, Markdown rendering, command selector/model picker, status polling, Vietnamese labels, and keyboard behavior.
- Modify `internal/entry/webhost/static.go`: serve the embedded `markdown.js` asset.
- Modify `README.md` only if the final browser port/tutorial wording needs to match the user-owned Compose port change; do not overwrite that change.

## Task 1: Lock the command catalog and slash parser

**Files:**
- Create: `internal/entry/webhost/commands.go`
- Test: `internal/entry/webhost/commands_test.go`

- [ ] **Step 1: Write failing catalog/parser tests.**

Add tests that require exactly these web-visible names and usage strings:

```go
func TestWebCommandCatalogMatchesApprovedTUICommands(t *testing.T) {
	got := commandCatalog()
	want := map[string]string{
		"model":    "/model [vai-trò]",
		"diag":    "/diag",
		"export":  "/export",
		"import":  "/import <đường-dẫn>",
		"simulate": "/simulate",
		"cocreate": "/cocreate",
	}
	if len(got) != len(want) {
		t.Fatalf("catalog length = %d, want %d: %#v", len(got), len(want), got)
	}
	for _, item := range got {
		if want[item.Name] != item.Usage {
			t.Fatalf("/%s usage = %q, want %q", item.Name, item.Usage, want[item.Name])
		}
	}
}

func TestParseSlashCommandPreservesTUIArguments(t *testing.T) {
	got, err := parseSlashCommand("/export ~/ten-truyen.epub from=3 to=8 --overwrite")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "export" || !reflect.DeepEqual(got.Args, []string{"~/ten-truyen.epub", "from=3", "to=8", "--overwrite"}) {
		t.Fatalf("parsed command = %#v", got)
	}
}

func TestParseSlashCommandRejectsHelpAndUnknownCommands(t *testing.T) {
	for _, input := range []string{"/help", "/start", "continue", "/"} {
		if _, err := parseSlashCommand(input); err == nil {
			t.Fatalf("parseSlashCommand(%q) unexpectedly succeeded", input)
		}
	}
}
```

- [ ] **Step 2: Run the focused tests and verify they fail.**

Run:

```bash
GOCACHE=/tmp/ainovel-webhost-plan-cache go test ./internal/entry/webhost -run 'Test(WebCommandCatalog|ParseSlashCommand)' -count=1
```

Expected: FAIL because the catalog and parser do not exist yet.

- [ ] **Step 3: Implement the minimal catalog/parser.**

Define `webCommandSpec`, `slashCommand`, and `parseSlashCommand` in `commands.go`. Keep `/help` out of `commandCatalog`; accept only a trimmed string beginning with `/`, lowercase the command name, preserve `strings.Fields` arguments, and return Vietnamese validation errors. The descriptions must be the approved Vietnamese copy, including `/diag` and `/cocreate`; do not add `start`, `steer`, `continue`, `rewrite`, or `importsim` to the web catalog.

- [ ] **Step 4: Run the focused tests and verify they pass.**

Run the command from Step 2. Expected: PASS.

- [ ] **Step 5: Commit the isolated command catalog change.**

```bash
git add internal/entry/webhost/commands.go internal/entry/webhost/commands_test.go
git commit -m "feat: define web TUI command catalog"
```

## Task 2: Expose the existing model selections and command request contract

**Files:**
- Modify: `internal/entry/webhost/commands.go`
- Modify: `internal/entry/webhost/server.go`
- Test: `internal/entry/webhost/commands_test.go`
- Test: `internal/entry/webhost/server_test.go`

- [ ] **Step 1: Write failing model catalog and request tests.**

Add a fake runtime capability with configured providers/models and tests for `GET /models` and model selection:

```go
func TestModelsReturnsTUIRolesAndConfiguredModels(t *testing.T) {
	rt := newFakeRuntime()
	rt.providers = []string{"openrouter", "ollama"}
	rt.models = map[string][]string{
		"openrouter": {"google/gemini-2.5-pro"},
		"ollama":     {"qwen3.5:27b"},
	}
	app := newServer(rt, 8)
	defer app.Close()

	response := getJSON(t, app, "/models?role=writer")
	if response.Role != "writer" || !slices.Equal(response.Roles, []string{"default", "coordinator", "architect", "writer", "editor"}) {
		t.Fatalf("roles = %#v", response.Roles)
	}
	if len(response.Providers) != 2 || response.Current.Provider == "" {
		t.Fatalf("model catalog = %#v", response)
	}
}

func TestCommandModelUsesSelectedProviderAndModel(t *testing.T) {
	rt := newFakeRuntimeWithCommandCapabilities()
	app := newServer(rt, 8)
	defer app.Close()

	response := postJSON(t, app, "/commands", map[string]any{
		"action": "command", "text": "/model writer",
		"provider": "openrouter", "model": "google/gemini-2.5-pro",
	})
	assertJSONOK(t, response, http.StatusOK)
	if rt.switchedRole != "writer" || rt.switchedProvider != "openrouter" || rt.switchedModel != "google/gemini-2.5-pro" {
		t.Fatalf("switch = %q/%q/%q", rt.switchedRole, rt.switchedProvider, rt.switchedModel)
	}
}
```

- [ ] **Step 2: Run the focused tests and verify they fail.**

```bash
GOCACHE=/tmp/ainovel-webhost-plan-cache go test ./internal/entry/webhost -run 'Test(Models|CommandModel)' -count=1
```

Expected: FAIL because `/models`, capability DTOs, and `action: command` are absent.

- [ ] **Step 3: Add narrow capability interfaces and the model endpoint.**

Keep the existing lifecycle `runtime` interface intact for current fakes. Add optional capability interfaces implemented by `*host.Host`:

```go
type modelRuntime interface {
	ConfiguredProviders() []string
	ConfiguredModels(string) []string
	CurrentModelSelection(string) (string, string, bool)
	SwitchModel(string, string, string) error
}
```

Add `GET /models`, normalize only `default`, `coordinator`, `architect`, `writer`, and `editor`, and return configured provider/model pairs plus the current selection. Return `501` when the runtime lacks the capability and `400` for an invalid role. Extend `commandRequest` with `Provider`, `Model`, and `Mode` while retaining existing action/text lifecycle payloads. An empty `Mode` remains `quick` for compatibility.

- [ ] **Step 4: Dispatch `/model` without inventing a command.**

For `action: command`, parse the slash text. `/model` must require the selected provider/model in the request and call `SwitchModel(role, provider, model)`. Publish a Vietnamese `command_result` event containing the exact slash command and success text after the core method succeeds. Reject `/help`, lifecycle names, unknown names, invalid role, or missing selector fields with a Vietnamese error.

For the existing `action: start`, accept `mode` values `quick` and `cocreate`. Keep `quick` mapped to `startup.PrepareQuick`; route `cocreate` into the cold co-create session described in Task 3 instead of bypassing the TUI clarification flow.

- [ ] **Step 5: Run the focused tests and verify they pass.**

Run the commands from Step 2. Expected: PASS.

- [ ] **Step 6: Commit the model/API contract change.**

```bash
git add internal/entry/webhost/commands.go internal/entry/webhost/server.go internal/entry/webhost/commands_test.go internal/entry/webhost/server_test.go
git commit -m "feat: expose web model selector contract"
```

## Task 3: Implement command result/progress SSE adapters

**Files:**
- Modify: `internal/entry/webhost/commands.go`
- Modify: `internal/entry/webhost/server.go`
- Test: `internal/entry/webhost/commands_test.go`
- Test: `internal/entry/webhost/sse_test.go`

- [ ] **Step 1: Write failing result/progress tests.**

Test the existing core methods through the web command adapter:

```go
func TestCommandExportPublishesMarkdownResult(t *testing.T) {
	rt := newFakeRuntimeWithCommandCapabilities()
	app := newServer(rt, 8)
	defer app.Close()

	response := postJSON(t, app, "/commands", map[string]any{"action": "command", "text": "/export"})
	assertJSONOK(t, response, http.StatusAccepted)
	result := waitForFrame(t, app.hub, "command_result")
	var payload commandResultFrame
	if err := json.Unmarshal(result.Data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Command != "/export" || !strings.Contains(payload.Markdown, "Đã xuất truyện") || !payload.Done {
		t.Fatalf("result = %#v", payload)
	}
}

func TestCommandImportPublishesProgressAndTerminalResult(t *testing.T) {
	rt := newFakeRuntimeWithCommandCapabilities()
	rt.importEvents = []imp.Event{{Message: "Đang phân đoạn", Current: 1, Total: 2}, {Message: "Hoàn tất", Stage: imp.StageDone, Current: 2, Total: 2}}
	app := newServer(rt, 8)
	defer app.Close()

	response := postJSON(t, app, "/commands", map[string]any{"action": "command", "text": "/import book.epub"})
	assertJSONOK(t, response, http.StatusAccepted)
	progress := waitForFrame(t, app.hub, "command_progress")
	if !bytes.Contains(progress.Data, []byte("Đang phân đoạn")) {
		t.Fatalf("progress = %s", progress.Data)
	}
	result := waitForFrame(t, app.hub, "command_result")
	if !bytes.Contains(result.Data, []byte("Hoàn tất")) {
		t.Fatalf("result = %s", result.Data)
	}
}
```

- [ ] **Step 2: Run the tests and verify they fail.**

```bash
GOCACHE=/tmp/ainovel-webhost-plan-cache go test ./internal/entry/webhost -run 'TestCommand(Export|Import)' -count=1
```

Expected: FAIL because `command_result`/`command_progress` and the extended runtime operations do not exist.

- [ ] **Step 3: Add explicit SSE payload types.**

Use JSON fields that are stable for the browser:

```go
type commandResultFrame struct {
	Command  string `json:"command"`
	Markdown string `json:"markdown"`
	Level    string `json:"level"`
	Done     bool   `json:"done"`
}

type commandProgressFrame struct {
	Command string `json:"command"`
	Text    string `json:"text"`
	Stage   string `json:"stage,omitempty"`
	Current int    `json:"current,omitempty"`
	Total   int    `json:"total,omitempty"`
	Level   string `json:"level"`
}
```

Publish these through `s.hub`; keep existing replay IDs and `Last-Event-ID` behavior unchanged.

- [ ] **Step 4: Add adapters for diag, export, import, and simulate.**

Use the existing core APIs rather than duplicating their logic:

- `/diag`: build `store.NewStore(rt.Dir())`, call `diag.Diagnose`, `diag.WriteExport`, and publish a Markdown report using `diag.RenderExport` plus the export path.
- `/export`: parse the same `path`, `from=N`, `to=N`, and `--overwrite` syntax as TUI, call `Host.Export`, and publish the Vietnamese success summary or error.
- `/import`: parse `<đường-dẫn>` and optional `from=N`, call `Host.ImportFrom`, consume every `imp.Event`, publish progress, and publish a terminal result when the channel closes.
- `/simulate`: call `Host.Simulate`, consume every `sim.Event`, publish progress, and publish a terminal result when the channel closes.

The capability interfaces must expose only the existing method signatures needed by these adapters (`Dir`, `Export`, `ImportFrom`, and `Simulate`). Return `202 Accepted` once an asynchronous command has been accepted; return `400` for parser errors and `409`/`500` for core conflicts/errors according to the existing endpoint conventions.

- [ ] **Step 5: Add `/cocreate` session state and command actions.**

Add server-owned state protected by a mutex: stage flag, `[]host.CoCreateMessage` history, active request cancellation, and latest `host.CoCreateReply`. `/cocreate` calls `PauseForCoCreate`, seeds the exact TUI opener `Tôi tạm dừng một chút, muốn cùng bạn lên kế hoạch cho hướng đi tiếp theo.`, and uses `StageCoCreateStream`. A `start` request with `mode: "cocreate"` seeds the user's initial prompt and uses `CoCreateStream` without starting the main engine. A subsequent normal chat submit while the session is active uses an internal command action to append a user message and call the same stream with accumulated history. Publish `command_progress` for `thinking` and `reply`, then `command_result` containing reply Markdown, `Prompt`, `Ready`, and `Suggestions`. Add internal actions for apply (`StartPrepared(host.BuildStartPrompt(draft))` for cold mode or `ResumeFromCoCreate(draft)` for stage mode) and cancel (`CancelCoCreate`); these actions are UI controls, not additional slash commands.

- [ ] **Step 6: Run backend focused tests and race tests.**

```bash
GOCACHE=/tmp/ainovel-webhost-plan-cache go test ./internal/entry/webhost -run 'Test(Command|SSE|Status|Questions|Resume)' -count=1
GOCACHE=/tmp/ainovel-webhost-plan-cache go test -race ./internal/entry/webhost -run 'Test(Command|SSE|Status|Questions|Resume)' -count=1
```

Expected: PASS in an environment that permits loopback listeners; if the sandbox rejects listeners, record that limitation and still run non-listener command/parser tests.

- [ ] **Step 7: Commit the command adapter.**

```bash
git add internal/entry/webhost/commands.go internal/entry/webhost/server.go internal/entry/webhost/commands_test.go internal/entry/webhost/sse_test.go
git commit -m "feat: execute TUI commands through web SSE"
```

## Task 4: Add a safe Markdown renderer

**Files:**
- Create: `internal/entry/webhost/web/markdown.js`
- Create: `internal/entry/webhost/web/markdown.test.js`
- Modify: `internal/entry/webhost/static.go`

- [ ] **Step 1: Write failing Node tests.**

Use the Node `vm` loader to provide `window` and assert the renderer never emits raw HTML:

```js
test("renders headings, lists, quotes, code and tables", () => {
  const html = renderMarkdown("# Tiêu đề\n\n- Một\n- Hai\n\n> Ghi chú\n\n```go\nfmt.Println(1)\n```\n\n| A | B |\n|---|---|\n| 1 | 2 |");
  assert.match(html, /<h1>Tiêu đề<\\/h1>/);
  assert.match(html, /<ul>/);
  assert.match(html, /<blockquote>/);
  assert.match(html, /<pre><code class="language-go">/);
  assert.match(html, /<table>/);
});

test("escapes model-provided HTML and javascript links", () => {
  const html = renderMarkdown('<script>alert(1)</script>\n\n[bad](javascript:alert(1))');
  assert.doesNotMatch(html, /<script>/);
  assert.doesNotMatch(html, /javascript:/i);
  assert.match(html, /&lt;script&gt;/);
});
```

- [ ] **Step 2: Run tests and verify they fail.**

```bash
node --test internal/entry/webhost/web/markdown.test.js
```

Expected: FAIL because `markdown.js` does not exist.

- [ ] **Step 3: Implement the bounded renderer.**

Normalize CRLF, escape `&<>"'`, parse fenced code blocks before block lines, then render headings, horizontal rules, blockquotes, ordered/unordered lists, simple tables, paragraphs, and inline strong/emphasis/code/link. Validate link protocols against `http:`, `https:`, and `mailto:`; render invalid links as escaped text. Expose `window.AINovelMarkdown.render` and do not use `innerHTML` with untrusted input anywhere else.

- [ ] **Step 4: Serve and test the asset.**

Add `/markdown.js` to `handleStatic`. Run the Node test command and the existing static asset tests:

```bash
node --test internal/entry/webhost/web/markdown.test.js
GOCACHE=/tmp/ainovel-webhost-plan-cache go test ./internal/entry/webhost -run 'TestStatic' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit the Markdown renderer.**

```bash
git add internal/entry/webhost/web/markdown.js internal/entry/webhost/web/markdown.test.js internal/entry/webhost/static.go
git commit -m "feat: render streamed Markdown safely in web host"
```

## Task 5: Replace the HTML layout with the Vietnamese chat structure

**Files:**
- Modify: `internal/entry/webhost/web/index.html`
- Modify: `internal/entry/webhost/web/app.css`

- [ ] **Step 1: Replace the three lifecycle forms with one composer.**

The page must contain these stable IDs used by `app.js`: `status-strip`, `status-details`, `chat`, `command-palette`, `model-panel`, `question`, `question-form`, `composer-form`, `composer-input`, `send-button`, `pause-button`, `resume-button`, `startup-mode`, `reconnect-button`, and `server-error`. All visible copy is Vietnamese. No visible `Start`, `Steer`, or `Continue` labels and no `/help` button.

- [ ] **Step 2: Add the TUI-style status strip/details panel.**

The strip shows `Trạng thái`, `Giai đoạn`, `Luồng`, `Model`, context window, chapter progress, and connection state. The details panel has sections for `Tổng quan`, `Nhân vật đang chạy`, `Ngữ cảnh`, `Sử dụng`, `Bộ nhớ đệm`, `Viết lại`, and `Can thiệp` and is hidden until the user opens it.

- [ ] **Step 3: Add chat and command palette styles using existing tokens.**

Reuse `#10131a`, `#171c25`, `#0b0f15`, `#8fd3ff`, `#aeb8c7`, and existing border/focus rules. User messages remain compact right-aligned bubbles. Assistant Markdown, host events, tool output, and command results occupy full width. Code blocks scroll horizontally; long chat content wraps; the composer stays usable on narrow screens.

- [ ] **Step 4: Verify the static markup has no forbidden labels.**

```bash
rg -n 'Start story|Send direction|Continue|/help|Steer|Host question|Output|Status' internal/entry/webhost/web/index.html
```

Expected: no matches. Run `git diff --check` and inspect the HTML IDs before moving to JavaScript.

- [ ] **Step 5: Commit the layout/style change.**

```bash
git add internal/entry/webhost/web/index.html internal/entry/webhost/web/app.css
git commit -m "feat: add Vietnamese web chat layout"
```

## Task 6: Implement transcript, SSE, status, and command UX

**Files:**
- Modify: `internal/entry/webhost/web/app.js`

- [ ] **Step 1: Add explicit browser state and rendering helpers.**

Use a message list rather than one `<pre>` buffer:

```js
const state = {
  messages: [],
  streamingIndex: -1,
  commandItems: [],
  commandIndex: 0,
  pendingQuestion: null,
  detailsOpen: false,
  lastEventID: null,
  statusTimer: null,
};
```

Implement `addUserMessage`, `startAssistantMessage`, `appendAssistantDelta`, `addEventMessage`, `addCommandResult`, and `renderChat`. Assistant messages call `window.AINovelMarkdown.render`; user text uses `textContent`. While streaming, keep the assistant item full width and mark it `Đang phản hồi` until `terminal` or the next stream starts.

- [ ] **Step 2: Wire SSE event types without losing replay behavior.**

Keep the existing `lastEventID` and reconnect URL behavior. Map events as follows:

```text
stream_clear     -> startAssistantMessage()
stream_delta     -> appendAssistantDelta(text)
host_event       -> addEventMessage(category, agent, summary, level)
command_progress -> addEventMessage(command, text, stage/progress, level)
command_result   -> addCommandResult(markdown, command, done, level)
terminal         -> finish current assistant message + refreshStatus()
question         -> render Vietnamese question panel
reset            -> refreshStatus()
```

Do not use `innerHTML` for payloads except the output of the safe Markdown renderer.

- [ ] **Step 3: Add slash palette and model picker.**

Define the exact Vietnamese command catalog in the browser with usage/description matching the server catalog. When the input starts with `/`, filter the palette; selecting an item inserts its TUI usage. Selecting `/model` opens the model panel and loads `/models?role=default`. Submitting the model panel sends `action: "command", text: "/model <role>", provider, model`. Other commands send their exact slash text through the same endpoint. Do not send UI aliases.

- [ ] **Step 4: Wire free-text submit and internal co-create actions.**

When not in a command or model panel, Enter sends a user message. For the first free-text request send `action: "start"` with `mode: "quick"` or `mode: "cocreate"` from the startup-mode control; the UI must label these as `Bắt đầu nhanh` and `Đồng sáng tác`, never `Start`. While running use the existing `steer` action, while paused/completed use `continue`, but do not expose those words as UI concepts. When a saved workspace is reported by `RecoveryLabel`, show a Vietnamese `Tiếp tục khôi phục` control that sends the existing `resume` action. While co-create is active, send the internal co-create message action and render its reply/draft/suggestions; expose Vietnamese `Áp dụng và tiếp tục` and `Thoát đồng sáng tác` controls.

- [ ] **Step 5: Render the full UISnapshot and poll it.**

Map all status labels in Vietnamese (`running` → `Đang chạy`, `writing` → `Viết`, `reviewing` → `Đánh giá`, `rewriting` → `Viết lại`, `polishing` → `Đánh bóng`, `paused` → `Đã tạm dừng`, `completed` → `Đã hoàn thành`). Render provider/model/context window, chapter progress, word count, active agents/tool, context percentage/tokens, token usage, cost/budget, cache, pending rewrites, pending steer, and recovery label. Start a 3-second timer after initial load and clear it on page unload.

- [ ] **Step 6: Implement the approved keyboard behavior.**

Enter submits, Tab toggles the startup mode control, Esc clears the input and closes the active palette/model/detail panel, and Ctrl+C is represented by a `Thoát ứng dụng`/stop control with the browser-safe explanation that progress is saved. Do not intercept browser Ctrl+C globally.

- [ ] **Step 7: Run static checks and a browser smoke check.**

```bash
node --check internal/entry/webhost/web/app.js
node --check internal/entry/webhost/web/markdown.js
rg -n 'Start|Steer|Continue|/help|Could not|The |Request failed|Output|Status|Host question' internal/entry/webhost/web
git diff --check
```

Expected: JavaScript syntax passes; the only remaining English matches, if any, are protocol names/code comments rather than visible UI strings. Start the web host in an environment that permits a listener, open the page, submit a short prompt, verify one user bubble plus a full-width Markdown assistant response, open `/` palette, open model details, and verify status changes without refresh.

- [ ] **Step 8: Commit the browser behavior.**

```bash
git add internal/entry/webhost/web/app.js
git commit -m "feat: stream web host output as Vietnamese chat"
```

## Task 7: Update operator documentation without touching user-owned Compose changes

**Files:**
- Modify: `README.md` only where the browser URL and UI instructions are now stale.
- Modify: `docs/web-host.md` only where the browser URL and chat behavior need explanation.

- [ ] **Step 1: Update the Vietnamese tutorial.**

Describe the single composer, `/` selector, exact command list, Markdown response stream, status details panel, and Vietnamese keyboard controls. Use the actual host port from the current Compose file (`8888:8080`) if that user-owned mapping remains present; do not edit the mapping itself.

- [ ] **Step 2: Check documentation against the UI and Compose config.**

```bash
docker compose config
rg -n 'localhost:|/model|/diag|/export|/import|/simulate|/cocreate|Start story|Steer|Continue|/help' README.md docs/web-host.md
git diff --check
```

Expected: documented URL matches the configured host port, command names are exact, and forbidden UI labels are absent from the web instructions.

- [ ] **Step 3: Commit only documentation edits.**

```bash
git add README.md docs/web-host.md
git commit -m "docs: describe Vietnamese web chat workflow"
```

## Task 8: Full verification and review handoff

**Files:**
- Verify all changed files; no new source changes unless a failing check identifies a concrete defect.

- [ ] **Step 1: Format and run focused backend checks.**

```bash
gofmt -w internal/entry/webhost/*.go
GOCACHE=/tmp/ainovel-webhost-plan-cache go test ./internal/entry/webhost -count=1
GOCACHE=/tmp/ainovel-webhost-plan-cache go test -race ./internal/entry/webhost -count=1
GOCACHE=/tmp/ainovel-webhost-plan-cache go vet ./internal/entry/webhost
```

Expected: focused tests, race tests, and vet pass unless the sandbox blocks loopback listener tests; distinguish that environmental failure from code failures.

- [ ] **Step 2: Run JavaScript and static checks.**

```bash
node --test internal/entry/webhost/web/markdown.test.js
node --check internal/entry/webhost/web/app.js
git diff --check
```

- [ ] **Step 3: Build the binary and Docker image.**

```bash
GOCACHE=/tmp/ainovel-webhost-plan-cache go build ./cmd/ainovel-web-host
docker compose config
docker build -t ainovel-web-host:local .
```

Expected: build succeeds; Compose still maps the user-selected host port to container port `8080`; the image contains the embedded Vietnamese chat assets.

- [ ] **Step 4: Run final diff/status audit.**

```bash
git status --short
git diff HEAD~8 --stat
git diff --check
rg -n 'Start story|Send direction|Continue|Steer|/help' internal/entry/webhost/web README.md docs/web-host.md
```

Expected: no forbidden visible UI labels; unrelated user changes remain intact and are reported separately.

- [ ] **Step 5: Request code review.**

Run the repository review workflow against the final diff, specifically checking command parity with TUI, safe Markdown rendering, SSE replay/reconnect, status freshness, race safety, and preservation of the current color tokens.
