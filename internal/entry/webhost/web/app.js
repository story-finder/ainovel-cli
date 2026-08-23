(() => {
  "use strict";

  const commands = [
    { name: "model", usage: "/model [vai-trò]", description: "Chuyển đổi mô hình mặc định hoặc theo vai trò" },
    { name: "diag", usage: "/diag", description: "Chẩn đoán tình trạng sáng tác tiểu thuyết" },
    { name: "export", usage: "/export", description: "Xuất truyện các chương đã hoàn thành sang TXT/EPUB" },
    { name: "import", usage: "/import <đường-dẫn>", description: "Nhập truyện bên ngoài để tiếp tục viết" },
    { name: "simulate", usage: "/simulate", description: "Đọc ./simulate để tạo hoặc cập nhật tăng dần hồ sơ mô phỏng phong cách viết" },
    { name: "cocreate", usage: "/cocreate", description: "Tạm dừng sáng tác, đồng sáng tác lên kế hoạch cho các giai đoạn tiếp theo" },
  ];
  const roles = ["default", "coordinator", "architect", "writer", "editor"];
  const roleLabels = {
    default: "Mặc định",
    coordinator: "Điều phối viên",
    architect: "Kiến trúc sư",
    writer: "Người viết",
    editor: "Biên tập viên",
  };
  const statusLabelMap = {
    ready: "Sẵn sàng",
    running: "Đang chạy",
    review: "Đánh giá",
    rewrite: "Viết lại",
    complete: "Hoàn tất",
    paused: "Tạm dừng",
  };
  const phaseLabels = {
    init: "Khởi tạo",
    premise: "Tiền đề",
    outline: "Đề cương",
    writing: "Viết",
    complete: "Hoàn tất",
  };
  const flowLabels = {
    writing: "Viết",
    reviewing: "Đánh giá",
    rewriting: "Viết lại",
    polishing: "Đánh bóng",
    steering: "Điều chỉnh",
  };
  const runtimeStateLabels = {
    idle: "Đang chờ",
    running: "Đang chạy",
    pausing: "Đang tạm dừng",
    paused: "Đã tạm dừng",
    completed: "Đã hoàn thành",
  };
  const agentStateLabels = {
    idle: "Đang chờ",
    working: "Đang làm việc",
    thinking: "Đang suy nghĩ",
    tool: "Đang dùng công cụ",
    complete: "Đã hoàn tất",
    error: "Gặp lỗi",
  };
  const agentLabels = {
    coordinator: "Điều phối viên",
    architect: "Kiến trúc sư",
    architect_long: "Kiến trúc sư dài hạn",
    architect_short: "Kiến trúc sư ngắn hạn",
    writer: "Người viết",
    editor: "Biên tập viên",
    subagent: "Tác vụ phụ",
  };
  const commandLabels = {
    cocreate: "Đồng sáng tác",
    cocreate_apply: "Áp dụng đồng sáng tác",
    cocreate_cancel: "Thoát đồng sáng tác",
    model: "Mô hình",
    diag: "Chẩn đoán",
    export: "Xuất truyện",
    import: "Nhập truyện",
    simulate: "Mô phỏng",
  };
  let latestStatusRequestID = 0;
  let questionRevision = 0;
  let lastEventID = null;
  const state = {
    messages: [{ kind: "assistant", text: "Sẵn sàng đồng hành cùng bạn. Hãy mô tả ý tưởng, nhân vật hoặc cảnh mở đầu để bắt đầu." }],
    streamingIndex: -1,
    commandIndex: 0,
    pendingQuestion: null,
    statusTimer: null,
    status: null,
    started: false,
    coCreateActive: false,
    coCreatePending: false,
    detailsOpen: false,
    eventSource: null,
  };
  const elements = {};

  const get = (object, ...names) => {
    if (!object || typeof object !== "object") return undefined;
    for (const name of names) {
      if (name in object) return object[name];
      const found = Object.keys(object).find((key) => key.toLowerCase() === name.toLowerCase());
      if (found) return object[found];
    }
    return undefined;
  };
  const field = get;
  const text = (value, fallback = "") => value === undefined || value === null || value === "" ? fallback : String(value);
  const number = (value, fallback = 0) => Number.isFinite(Number(value)) ? Number(value) : fallback;
  const formatNumber = (value) => number(value).toLocaleString("vi-VN");
  const formatMoney = (value) => `${number(value).toFixed(4)} đô la Mỹ`;
  const formatError = (error, fallback = "Đã xảy ra lỗi.") => error && error.message ? error.message : fallback;
  const translate = (value, labels, fallback = "Chưa có dữ liệu") => {
    const key = String(value || "").trim().toLowerCase();
    if (!key) return fallback;
    return labels[key] || "Chưa xác định";
  };
  const commandLabel = (value) => translate(value, commandLabels, "Máy chủ");
  const agentLabel = (value) => translate(value, agentLabels, "Tác vụ");
  const roleLabel = (value) => translate(value, roleLabels, "Vai trò khác");

  function rememberEventID(event) {
    const raw = event && event.lastEventId;
    if (/^\d+$/.test(raw || "")) lastEventID = Number(raw);
  }

  function setError(message) {
    elements.serverError.textContent = message ? String(message) : "";
  }

  async function requestJSON(url, options) {
    const response = await fetch(url, options);
    let payload = null;
    try { payload = await response.json(); } catch (_) { payload = null; }
    if (!response.ok) throw new Error(text(get(payload, "error", "Error"), `Máy chủ trả về lỗi ${response.status}.`));
    return payload || {};
  }

  function messageNode(message) {
    const article = document.createElement("article");
    article.className = `message message-${message.kind || "event"}`;
    if (message.streaming) article.classList.add("is-streaming");
    const meta = document.createElement("div");
    meta.className = "message-meta";
    const author = document.createElement("span");
    author.textContent = message.author || (message.kind === "user" ? "Bạn" : "AI Novel");
    const label = document.createElement("span");
    label.textContent = message.label || (message.kind === "user" ? "Yêu cầu" : "Phản hồi");
    meta.append(author, label);
    const content = document.createElement("div");
    content.className = `message-content ${message.kind === "user" ? "user-text" : "assistant-markdown"}`;
    if (message.kind === "user") {
      content.textContent = message.text || "";
    } else if (message.markdown || message.text) {
      const renderer = window.AINovelMarkdown && window.AINovelMarkdown.render;
      if (renderer) content.innerHTML = renderer(message.markdown || message.text || "");
      else content.textContent = message.markdown || message.text || "";
    }
    article.append(meta, content);
    if (message.actions) appendActions(article, message.actions);
    return article;
  }

  function appendActions(article, actions) {
    const wrap = document.createElement("div");
    wrap.className = "message-actions";
    actions.forEach((action) => {
      const button = document.createElement("button");
      button.type = "button";
      button.className = action.secondary ? "secondary" : "";
      button.textContent = action.label;
      button.addEventListener("click", action.handler);
      wrap.appendChild(button);
    });
    article.appendChild(wrap);
  }

  function renderChat() {
    elements.transcript.replaceChildren(...state.messages.map(messageNode));
    elements.transcript.scrollTop = elements.transcript.scrollHeight;
  }

  function addUserMessage(value) {
    const valueText = String(value || "").trim();
    if (!valueText) return;
    state.messages.push({ kind: "user", text: valueText });
    renderChat();
  }

  function startAssistantMessage() {
    if (state.streamingIndex >= 0) state.messages[state.streamingIndex].streaming = false;
    state.messages.push({ kind: "assistant", markdown: "", streaming: true, label: "Đang phản hồi" });
    state.streamingIndex = state.messages.length - 1;
    renderChat();
  }

  function appendAssistantDelta(delta) {
    if (typeof delta !== "string" || !delta) return;
    if (state.streamingIndex < 0) startAssistantMessage();
    state.messages[state.streamingIndex].markdown += delta;
    renderChat();
  }

  function finishAssistant() {
    if (state.streamingIndex >= 0) {
      state.messages[state.streamingIndex].streaming = false;
      state.messages[state.streamingIndex].label = "Phản hồi hoàn tất";
      state.streamingIndex = -1;
      renderChat();
    }
  }

  function addEventMessage(author, content, level = "info", label = "Thông tin") {
    if (!content) return;
    state.messages.push({ kind: level === "error" ? "event" : "progress", author: text(author, "Máy chủ"), label, markdown: String(content) });
    renderChat();
  }

  function addCommandResult(payload) {
    const markdown = text(get(payload, "markdown", "Markdown"));
    const error = text(get(payload, "error", "Error"));
    const ready = Boolean(get(payload, "ready", "Ready"));
    const prompt = text(get(payload, "prompt", "Prompt"));
    const suggestions = get(payload, "suggestions", "Suggestions");
    const command = text(get(payload, "command", "Command"));
    if (prompt || command === "/cocreate" || command === "cocreate") state.coCreateActive = true;
    if (command === "cocreate_apply" && !error) {
      state.coCreatePending = false;
      state.coCreateActive = false;
    }
    if (command === "cocreate_apply" && error) state.coCreatePending = false;
    if (command === "cocreate_cancel" && !error) {
      state.coCreatePending = false;
      state.coCreateActive = false;
    }
    if (command === "cocreate" && error) {
      state.coCreatePending = false;
      state.coCreateActive = false;
    }
    const resultSections = [];
    if (markdown || error) resultSections.push(markdown || error);
    if (prompt) resultSections.push(`## Bản nháp chỉ thị\n\n${prompt}`);
    const actions = [];
    if (ready && prompt) actions.push({ label: "Áp dụng và tiếp tục", handler: () => postInternal("cocreate_apply", prompt) });
    if (state.coCreateActive) actions.push({ label: "Thoát đồng sáng tác", secondary: true, handler: () => postInternal("cocreate_cancel", "") });
    state.messages.push({
      kind: "result",
      author: commandLabel(command),
      label: error ? "Lỗi" : "Kết quả lệnh",
      markdown: resultSections.join("\n\n"),
      actions: actions.length ? actions : undefined,
    });
    if (Array.isArray(suggestions)) addSuggestions(suggestions);
    renderChat();
  }

  function addSuggestions(suggestions) {
    const valid = suggestions.map((item) => String(item || "").trim()).filter(Boolean).slice(0, 3);
    if (!valid.length) return;
    state.messages.push({ kind: "event", author: "Gợi ý", label: "Bạn có thể nói tiếp", markdown: valid.map((item, index) => `${index + 1}. ${item}`).join("\n") });
  }

  function parseEvent(event) {
    try { return JSON.parse(event.data); } catch (_) { throw new Error("Dữ liệu từ máy chủ không hợp lệ."); }
  }

  function onEvent(event, handler) {
    elements.eventSource.addEventListener(event, (message) => {
      rememberEventID(message);
      try { handler(parseEvent(message), message); } catch (error) { setError(formatError(error, "Không thể đọc sự kiện máy chủ.")); }
    });
  }

  function replayRuntime(item) {
    const kind = text(field(item, "kind", "Kind")).toLowerCase();
    const payload = field(item, "payload", "Payload") || {};
    if (kind === "stream_clear") {
      startAssistantMessage();
      return;
    }
    if (kind === "stream_delta") {
      appendAssistantDelta(text(field(payload, "delta", "Delta"), field(payload, "text", "Text")));
    }
  }

  function connectEvents() {
    if (state.eventSource) state.eventSource.close();
    const eventURL = lastEventID === null ? "/events" : `/events?after=${encodeURIComponent(lastEventID)}`;
    const source = new EventSource(eventURL);
    const eventSource = source;
    state.eventSource = eventSource;
    elements.eventSource = eventSource;
    source.addEventListener("open", () => setError(""));
    onEvent("stream_clear", () => startAssistantMessage());
    onEvent("stream_delta", (payload) => appendAssistantDelta(text(get(payload, "text", "Text"))));
    onEvent("host_event", (payload) => {
      const summary = text(get(payload, "summary", "Summary"), text(get(payload, "detail", "Detail")));
      addEventMessage(text(get(payload, "agent", "Agent"), text(get(payload, "category", "Category"), "Máy chủ")), summary, text(get(payload, "level", "Level"), "info"), "Sự kiện");
      refreshStatus();
    });
    onEvent("command_progress", (payload) => {
      const current = number(get(payload, "current", "Current"));
      const total = number(get(payload, "total", "Total"));
      let progress = text(get(payload, "text", "Text"));
      if (total) progress += ` (${current}/${total})`;
      addEventMessage(text(get(payload, "command", "Command"), "Lệnh"), progress, text(get(payload, "level", "Level")), "Đang xử lý");
    });
    onEvent("command_result", (payload) => { addCommandResult(payload); refreshStatus(); });
    onEvent("terminal", (payload) => { finishAssistant(); renderStatus({ host: payload }); refreshStatus(); });
    onEvent("question", (payload) => renderQuestionFrame(payload));
    onEvent("reset", () => { finishAssistant(); refreshStatus(); });
    onEvent("runtime_replay", replayRuntime);
    eventSource.addEventListener("heartbeat", rememberEventID);
    eventSource.addEventListener("runtime_replay", rememberEventID);
    source.addEventListener("error", () => setError("Luồng sự kiện đã ngắt, đang thử kết nối lại…"));
  }

  function runtimeLabel(value) { return translate(value, runtimeStateLabels); }
  function stateLabel(value) { return translate(value, agentStateLabels); }
  function statusLabel(value) { return translate(value, statusLabelMap); }
  function phaseLabel(value) { return translate(value, phaseLabels); }
  function flowLabel(value) { return translate(value, flowLabels); }

  function setField(id, value) { if (elements[id]) elements[id].textContent = text(value, "Chưa có dữ liệu"); }
  function percentage(value) { return `${Math.max(0, Math.min(100, number(value))).toFixed(1)}%`; }

  function renderStatus(payload, requestID, requestRevision) {
    if (requestID !== undefined) {
      if (requestID !== latestStatusRequestID || requestRevision !== questionRevision) {
        return;
      }
    }
    const snapshot = get(payload, "host", "Host") || payload || {};
    state.status = snapshot;
    const coCreate = get(payload, "cocreate", "CoCreate");
    if (coCreate && typeof coCreate === "object") {
      state.coCreateActive = state.coCreatePending || Boolean(get(coCreate, "active", "Active")) || Boolean(get(coCreate, "inFlight", "InFlight"));
    }
    const runtimeState = String(get(snapshot, "runtimeState", "RuntimeState") || "").toLowerCase();
    state.started = Boolean(get(snapshot, "isRunning", "IsRunning")) || ["running", "writing", "reviewing", "rewriting", "polishing"].includes(runtimeState);
    const runtime = runtimeLabel(runtimeState);
    const translatedStatus = statusLabel(get(snapshot, "statusLabel", "StatusLabel"));
    setField("statusText", ["paused", "pausing", "completed"].includes(runtimeState) ? runtime : translatedStatus === "Chưa xác định" || translatedStatus === "Chưa có dữ liệu" ? runtime : translatedStatus);
    setField("statusPhase", phaseLabel(get(snapshot, "phase", "Phase")));
    setField("statusThread", flowLabel(get(snapshot, "flow", "Flow")));
    setField("statusModel", get(snapshot, "modelName", "ModelName"));
    const current = number(get(snapshot, "currentChapter", "CurrentChapter"));
    const total = number(get(snapshot, "totalChapters", "TotalChapters"));
    setField("statusProgress", total ? `${current}/${total}` : current || "Chưa có dữ liệu");
    setField("statusProvider", get(snapshot, "provider", "Provider"));
    setField("statusModelDetail", `${text(get(snapshot, "modelName", "ModelName"))} · ${formatNumber(get(snapshot, "modelContextWindow", "ModelContextWindow"))} token`);
    setField("statusStyle", get(snapshot, "style", "Style"));
    setField("statusConnection", "Đã kết nối");
    const agents = get(snapshot, "agents", "Agents");
    const active = Array.isArray(agents) ? agents.find((agent) => String(get(agent, "state", "State")).toLowerCase() !== "idle") || agents[0] : null;
    setField("statusAgent", active ? `${agentLabel(get(active, "name", "Name"))} · ${stateLabel(get(active, "state", "State"))}` : "Không có");
    setField("statusTool", active ? get(active, "tool", "Tool") : "Không có");
    setField("statusChapter", total ? `${current}/${total} · ${formatNumber(get(snapshot, "totalWordCount", "TotalWordCount"))} từ` : "Chưa có dữ liệu");
    const contextWindow = get(snapshot, "contextWindow", "ContextWindow");
    setField("statusContext", `${formatNumber(get(snapshot, "contextTokens", "ContextTokens"))}/${formatNumber(contextWindow)} token`);
    setField("statusContextUsed", percentage(get(snapshot, "contextPercent", "ContextPercent")));
    setField("statusWritingStyle", get(snapshot, "style", "Style"));
    setField("statusUsage", `vào ${formatNumber(get(snapshot, "totalInputTokens", "TotalInputTokens"))} · ra ${formatNumber(get(snapshot, "totalOutputTokens", "TotalOutputTokens"))}`);
    setField("statusCost", `${formatMoney(get(snapshot, "totalCostUSD", "TotalCostUSD"))} · tiết kiệm ${formatMoney(get(snapshot, "totalSavedUSD", "TotalSavedUSD"))}`);
    setField("statusBudget", number(get(snapshot, "budgetLimitUSD", "BudgetLimitUSD")) ? formatMoney(get(snapshot, "budgetLimitUSD", "BudgetLimitUSD")) : "Chưa bật");
    setField("statusCache", get(snapshot, "overallCacheCapable", "OverallCacheCapable") ? "Có hỗ trợ" : "Chưa hỗ trợ");
    setField("statusCacheRead", formatNumber(get(snapshot, "totalCacheReadTokens", "TotalCacheReadTokens")));
    setField("statusCacheWrite", formatNumber(get(snapshot, "totalCacheWriteTokens", "TotalCacheWriteTokens")));
    const rewrites = get(snapshot, "pendingRewrites", "PendingRewrites");
    setField("statusRewrites", Array.isArray(rewrites) && rewrites.length ? rewrites.join(", ") : "Không có");
    setField("statusSteer", get(snapshot, "pendingSteer", "PendingSteer") || "Không có");
    const recovery = text(get(snapshot, "recoveryLabel", "RecoveryLabel"));
    setField("statusRecovery", recovery || "Không có");
    setField("progressText", total ? `${current}/${total} chương · ${formatNumber(get(snapshot, "completedCount", "CompletedCount"))} hoàn tất` : runtime);
    elements.progressBar.style.width = total ? `${Math.min(100, (current / total) * 100)}%` : "0%";
    elements.resumeButton.hidden = !recovery;
    elements.pauseButton.hidden = !state.started;
    elements.resumeButton.textContent = recovery ? "Tiếp tục khôi phục" : "Tiếp tục";
    if (recovery) addRecoveryNotice(recovery);
    if (payload && (Object.prototype.hasOwnProperty.call(payload, "pending") || Object.prototype.hasOwnProperty.call(payload, "Pending"))) {
      const pending = field(payload, "pending", "Pending");
      if (pending) {
        renderQuestionFrame(pending);
      } else if (state.pendingQuestion || !elements.question.hidden) {
        clearQuestionFrame();
      }
    }
  }

  let recoveryNotice = false;
  function addRecoveryNotice(label) {
    if (recoveryNotice) return;
    recoveryNotice = true;
    state.messages.push({ kind: "event", author: "Máy chủ", label: "Khôi phục", markdown: `Đã tìm thấy tiến độ đã lưu: **${label}**. Bạn có thể tiếp tục khôi phục.` });
    renderChat();
  }

  async function refreshStatus() {
    const requestID = ++latestStatusRequestID;
    const requestRevision = questionRevision;
    try {
      const payload = await requestJSON("/status");
      if (requestID === latestStatusRequestID && requestRevision === questionRevision) {
        renderStatus(payload, requestID, requestRevision);
      }
    } catch (error) { if (requestID === latestStatusRequestID && requestRevision === questionRevision) setError(formatError(error, "Không thể tải trạng thái máy chủ.")); }
  }

  function toggleDetails(force) {
    state.detailsOpen = force === undefined ? !state.detailsOpen : force;
    elements.statusDetails.hidden = !state.detailsOpen;
    elements.statusDetailsToggle.setAttribute("aria-expanded", String(state.detailsOpen));
  }

  function renderPalette() {
    const query = elements.composerInput.value.trim().toLowerCase();
    if (!query.startsWith("/")) { elements.commandPalette.hidden = true; return; }
    const name = query.slice(1).split(/\s/)[0];
    const filtered = commands.filter((command) => !name || command.name.startsWith(name));
    elements.commandList.replaceChildren();
    filtered.forEach((command, index) => {
      const item = document.createElement("li");
      item.className = "command-entry";
      const button = document.createElement("button");
      button.type = "button";
      const code = document.createElement("code"); code.textContent = command.usage;
      const description = document.createElement("span"); description.textContent = command.description;
      button.append(code, description);
      button.addEventListener("click", () => selectCommand(command));
      item.appendChild(button); elements.commandList.appendChild(item);
      if (index === state.commandIndex) button.setAttribute("aria-current", "true");
    });
    state.commandItems = filtered;
    state.commandIndex = Math.min(state.commandIndex, Math.max(0, filtered.length - 1));
    elements.commandPalette.hidden = filtered.length === 0;
  }

  function selectCommand(command) {
    elements.commandPalette.hidden = true;
    if (command.name === "model") { elements.composerInput.value = "/model "; openModelPanel(); return; }
    elements.composerInput.value = command.name === "import" || command.name === "export" ? `${command.usage.split(" ")[0]} ` : command.usage;
    elements.composerInput.focus();
  }

  async function openModelPanel(role) {
    elements.modelPanel.hidden = false;
    await loadModels(role || elements.roleSelector.value || "default");
  }

  async function loadModels(role) {
    try {
      const payload = await requestJSON(`/models?role=${encodeURIComponent(role)}`);
      const list = get(payload, "roles", "Roles");
      if (Array.isArray(list)) {
        elements.roleSelector.replaceChildren();
        list.forEach((item) => { const option = document.createElement("option"); option.value = item; option.textContent = roleLabel(item); elements.roleSelector.appendChild(option); });
        elements.roleSelector.value = role;
      }
      elements.providerSelector.replaceChildren();
      const providers = get(payload, "providers", "Providers");
      state.modelProviders = Array.isArray(providers) ? providers : [];
      (Array.isArray(providers) ? providers : []).forEach((provider) => { const option = document.createElement("option"); option.value = text(get(provider, "provider", "Provider")); option.textContent = option.value; elements.providerSelector.appendChild(option); });
      populateModels();
      const current = get(payload, "current", "Current") || {};
      elements.providerSelector.value = text(get(current, "provider", "Provider"), elements.providerSelector.value);
      populateModels();
      elements.modelSelector.value = text(get(current, "model", "Model"), elements.modelSelector.value);
    } catch (error) { setError(formatError(error, "Không thể tải danh mục mô hình.")); }
  }

  function populateModels() {
    const provider = elements.providerSelector.value;
    const providers = state.modelProviders || [];
    const item = providers.find((value) => text(get(value, "provider", "Provider")) === provider);
    elements.modelSelector.replaceChildren();
    (item && Array.isArray(get(item, "models", "Models")) ? get(item, "models", "Models") : []).forEach((model) => { const option = document.createElement("option"); option.value = model; option.textContent = model; elements.modelSelector.appendChild(option); });
  }

  async function submitModel(event) {
    event.preventDefault();
    const role = elements.roleSelector.value || "default";
    const slash = role === "default" ? "/model" : `/model ${role}`;
    if (await postCommand(slash, { provider: elements.providerSelector.value, model: elements.modelSelector.value })) elements.modelPanel.hidden = true;
  }

  async function postCommand(command, extra = {}) {
    const coCreateCommand = command.trim().toLowerCase() === "/cocreate";
    if (command.trim().toLowerCase() === "/cocreate") state.coCreateActive = true;
    if (command.trim().toLowerCase() === "/cocreate") state.coCreatePending = true;
    addUserMessage(command);
    try { await requestJSON("/commands", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ action: "command", text: command, ...extra }) }); setError(""); return true; }
    catch (error) {
      if (coCreateCommand) { state.coCreatePending = false; state.coCreateActive = false; }
      setError(formatError(error, "Không thể thực hiện lệnh.")); return false;
    }
  }

  async function postInternal(action, value) {
    if (action === "cocreate_apply") state.coCreatePending = true;
    if (action === "cocreate_cancel") state.coCreatePending = true;
    try {
      await requestJSON("/commands", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ action, text: value }) });
      if (action === "cocreate_cancel") {
        state.coCreatePending = false;
        state.coCreateActive = false;
      }
      setError("");
      return true;
    } catch (error) {
      if (["cocreate_apply", "cocreate_cancel"].includes(action)) state.coCreatePending = false;
      setError(formatError(error, "Không thể thực hiện thao tác.")); return false;
    }
  }

  function freeTextAction() {
    if (state.coCreateActive || state.coCreatePending) return "cocreate_message";
    const runtimeState = String(get(state.status, "runtimeState", "RuntimeState") || "").toLowerCase();
    if (["paused", "completed"].includes(runtimeState)) return "continue";
    if (!state.status || runtimeState === "idle") return "start";
    return state.started ? "steer" : "start";
  }

  async function submitComposer(event) {
    event.preventDefault();
    const value = elements.composerInput.value.trim();
    if (!value) return;
    elements.composerInput.value = "";
    elements.commandPalette.hidden = true;
    if (value.startsWith("/")) {
      const modelCommand = value.match(/^\/model(?:\s|$)/);
      if (modelCommand) {
        const role = value.slice(modelCommand[0].length).trim().split(/\s+/)[0] || "default";
        elements.composerInput.value = role === "default" ? "/model " : `/model ${role}`;
        await openModelPanel(role);
        return;
      }
      await postCommand(value); return;
    }
    addUserMessage(value);
    const action = freeTextAction();
    const body = { action, text: value };
    if (action === "start") body.mode = elements.startupMode.value || "quick";
    const startingCoCreate = action === "start" && body.mode === "cocreate";
    const coCreateMessage = action === "cocreate_message";
    if (startingCoCreate) state.coCreateActive = true;
    if (startingCoCreate) state.coCreatePending = true;
    if (coCreateMessage) state.coCreatePending = true;
    try { await requestJSON("/commands", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }); setError(""); }
    catch (error) {
      if (startingCoCreate) { state.coCreatePending = false; state.coCreateActive = false; }
      if (coCreateMessage) state.coCreatePending = false;
      setError(formatError(error, "Không thể gửi yêu cầu."));
    }
  }

  function clearQuestionFrame() {
    questionRevision++;
    state.pendingQuestion = null;
    elements.questionItems.replaceChildren();
    elements.question.hidden = true;
    elements.questionError.textContent = "";
  }

  function sameQuestionFrame(frame) {
    const id = text(get(frame, "id", "ID"));
    const questions = get(frame, "questions", "Questions");
    return Boolean(state.pendingQuestion && !elements.question.hidden && id && Array.isArray(questions) && state.pendingQuestion.id === id && JSON.stringify(state.pendingQuestion.questions) === JSON.stringify(questions));
  }

  function renderQuestionFrame(frame) {
    if (sameQuestionFrame(frame)) return;
    const id = text(get(frame, "id", "ID"));
    const questions = get(frame, "questions", "Questions");
    if (!id || !Array.isArray(questions)) { setError("Câu hỏi từ máy chủ không hợp lệ."); return; }
    questionRevision++;
    state.pendingQuestion = { id, questions };
    elements.questionItems.replaceChildren();
    questions.forEach((question, index) => {
      const fieldset = document.createElement("fieldset");
      const legend = document.createElement("legend"); legend.textContent = text(get(question, "header", "Header"), `Câu hỏi ${index + 1}`) + `: ${text(get(question, "question", "Question"))}`; fieldset.appendChild(legend);
      const options = get(question, "options", "Options");
      (Array.isArray(options) ? options : []).forEach((item) => { const label = document.createElement("label"); label.className = "choice"; const input = document.createElement("input"); input.type = get(question, "multiSelect", "MultiSelect") ? "checkbox" : "radio"; input.name = `question-${index}`; input.value = text(get(item, "label", "Label")); label.append(input); const copy = document.createElement("span"); copy.textContent = text(get(item, "label", "Label")) + (get(item, "description", "Description") ? ` — ${get(item, "description", "Description")}` : ""); label.appendChild(copy); fieldset.appendChild(label); });
      const custom = document.createElement("label"); custom.className = "custom-answer"; custom.textContent = "Câu trả lời khác (không bắt buộc)"; const input = document.createElement("input"); input.type = "text"; input.dataset.custom = String(index); input.placeholder = "Nhập câu trả lời của bạn"; custom.appendChild(input); fieldset.appendChild(custom); elements.questionItems.appendChild(fieldset);
    });
    elements.question.hidden = false;
  }

  async function answerQuestion(event) {
    event.preventDefault(); if (!state.pendingQuestion) return;
    const answers = {}; const notes = {}; let invalid = false;
    state.pendingQuestion.questions.forEach((question, index) => {
      const questionText = text(get(question, "question", "Question"));
      const multiSelect = Boolean(get(question, "multiSelect", "MultiSelect"));
      let selected = [...elements.questionItems.querySelectorAll(`input[name="question-${index}"]:checked`)].map((item) => item.value);
      const custom = elements.questionItems.querySelector(`input[data-custom="${index}"]`);
      const customText = custom ? custom.value.trim() : "";
      if (customText) {
        if (multiSelect) {
          selected.push(customText);
        } else {
          selected = [customText];
        }
        notes[questionText] = customText;
      }
      if (!selected.length) invalid = true; else answers[questionText] = selected.join(", ");
    });
    if (invalid) { elements.questionError.textContent = "Bạn hãy trả lời tất cả câu hỏi."; return; }
    try { await requestJSON(`/questions/${encodeURIComponent(state.pendingQuestion.id)}/answer`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ answers, notes }) }); clearQuestionFrame(); } catch (error) { elements.questionError.textContent = formatError(error, "Không thể gửi lựa chọn."); }
  }

  function bind() {
    const elementIDs = {
      transcript: "transcript",
      serverError: "server-error",
      statusText: "status-text",
      statusPhase: "status-phase",
      statusThread: "status-thread",
      statusModel: "status-model",
      statusProgress: "status-progress",
      statusDetails: "status-details",
      statusDetailsToggle: "status-details-toggle",
      statusProvider: "status-provider",
      statusModelDetail: "status-model-detail",
      statusStyle: "status-style",
      statusConnection: "status-connection",
      statusAgent: "status-agent",
      statusTool: "status-tool",
      statusChapter: "status-chapter",
      statusContext: "status-context",
      statusContextUsed: "status-context-used",
      statusWritingStyle: "status-writing-style",
      statusUsage: "status-usage",
      statusCost: "status-cost",
      statusBudget: "status-budget",
      statusCache: "status-cache",
      statusCacheRead: "status-cache-read",
      statusCacheWrite: "status-cache-write",
      statusRewrites: "status-rewrites",
      statusSteer: "status-steer",
      statusRecovery: "status-recovery",
      progressText: "progress-text",
      progressBar: "progress-bar",
      resumeButton: "resume-button",
      pauseButton: "pause-button",
      reconnectButton: "reconnect-button",
      question: "question",
      questionItems: "question-items",
      questionError: "question-error",
      questionForm: "question-form",
      composerForm: "start-form",
      composerInput: "composer-input",
      commandPalette: "command-palette",
      startupMode: "startup-mode",
      modelPanel: "model-panel",
      modelForm: "model-form",
      roleSelector: "role-selector",
      providerSelector: "provider-selector",
      modelSelector: "model-selector",
      modelPanelClose: "model-panel-close",
    };
    Object.entries(elementIDs).forEach(([name, id]) => { elements[name] = document.getElementById(id); });
    elements.commandList = document.querySelector("#command-palette .command-list");
    elements.statusDetailsToggle.addEventListener("click", () => toggleDetails());
    renderChat();
    elements.composerForm.addEventListener("submit", submitComposer); elements.questionForm.addEventListener("submit", answerQuestion); elements.modelForm.addEventListener("submit", submitModel); document.getElementById("command-button").addEventListener("click", renderPalette); elements.modelPanelClose.addEventListener("click", () => { elements.modelPanel.hidden = true; }); elements.roleSelector.addEventListener("change", () => loadModels(elements.roleSelector.value)); elements.providerSelector.addEventListener("change", populateModels); elements.reconnectButton.addEventListener("click", connectEvents); elements.pauseButton.addEventListener("click", () => postInternal("pause", "")); elements.resumeButton.addEventListener("click", () => postInternal("resume", ""));
    elements.composerInput.addEventListener("input", renderPalette); elements.composerInput.addEventListener("keydown", (event) => { if (event.key === "Enter" && !event.shiftKey) { event.preventDefault(); elements.composerForm.requestSubmit(); } else if (event.key === "Tab") { event.preventDefault(); elements.startupMode.value = elements.startupMode.value === "quick" ? "cocreate" : "quick"; } else if (event.key === "Escape") { elements.composerInput.value = ""; elements.commandPalette.hidden = true; elements.modelPanel.hidden = true; toggleDetails(false); } });
    refreshStatus(); connectEvents(); state.statusTimer = window.setInterval(refreshStatus, 3000); window.addEventListener("beforeunload", () => { if (state.statusTimer) clearInterval(state.statusTimer); if (state.eventSource) state.eventSource.close(); });
  }

  document.addEventListener("DOMContentLoaded", bind);
})();
