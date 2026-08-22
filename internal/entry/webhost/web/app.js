(() => {
  "use strict";

  let eventSource = null;
  let pendingQuestion = null;

  const field = (value, lower, upper) => {
    if (!value || typeof value !== "object") {
      return undefined;
    }
    return value[lower] ?? value[upper];
  };

  const elements = {};

  function setServerError(message) {
    elements.serverError.textContent = message ? String(message) : "";
  }

  function setQuestionError(message) {
    elements.questionError.textContent = message ? String(message) : "";
  }

  function errorMessage(payload, fallback) {
    const message = field(payload, "error", "Error");
    return typeof message === "string" && message.trim() ? message : fallback;
  }

  async function requestJSON(url, options) {
    const response = await fetch(url, options);
    let payload = null;
    try {
      payload = await response.json();
    } catch (_error) {
      payload = null;
    }
    if (!response.ok) {
      throw new Error(errorMessage(payload, `Request failed (${response.status})`));
    }
    return payload;
  }

  function snapshotText(snapshot) {
    const state = field(snapshot, "RuntimeState", "runtimeState") || field(snapshot, "runtime_state", "RuntimeState");
    const label = field(snapshot, "StatusLabel", "statusLabel");
    const phase = field(snapshot, "Phase", "phase");
    const chapter = field(snapshot, "CurrentChapter", "currentChapter");
    const parts = [label, state, phase];
    if (chapter) {
      parts.push(`Chapter ${chapter}`);
    }
    return parts.filter((part) => part !== undefined && part !== null && String(part).trim()).map(String).join(" · ") || "Connected";
  }

  function renderStatus(payload) {
    const snapshot = field(payload, "host", "Host") || payload;
    elements.statusText.textContent = snapshotText(snapshot);
  }

  async function refreshStatus() {
    try {
      const payload = await requestJSON("/status");
      renderStatus(payload);
    } catch (error) {
      setServerError(error.message || "Could not load host status.");
    }
  }

  async function sendCommand(action, text) {
    try {
      await requestJSON("/commands", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ action, text })
      });
      setServerError("");
      await refreshStatus();
    } catch (error) {
      setServerError(error.message || "The command could not be completed.");
    }
  }

  function parseEvent(event) {
    try {
      return JSON.parse(event.data);
    } catch (_error) {
      throw new Error(`Invalid ${event.type || "server"} event.`);
    }
  }

  function updateStatusFromEvent(payload) {
    const summary = field(payload, "Summary", "summary");
    if (typeof summary === "string" && summary.trim()) {
      elements.statusText.textContent = summary;
      return;
    }
    elements.statusText.textContent = snapshotText(payload);
  }

  function handleQuestion(event) {
    let frame;
    try {
      frame = parseEvent(event);
    } catch (error) {
      setQuestionError(error.message);
      setServerError(error.message);
      return;
    }

    const id = field(frame, "id", "ID");
    const questions = field(frame, "questions", "Questions");
    if (typeof id !== "string" || !Array.isArray(questions)) {
      const message = "The server sent an invalid question.";
      setQuestionError(message);
      setServerError(message);
      return;
    }

    pendingQuestion = { id, questions };
    renderQuestions(questions);
    setQuestionError("");
    elements.question.hidden = false;
  }

  function renderQuestions(questions) {
    elements.questionItems.replaceChildren();
    questions.forEach((question, questionIndex) => {
      const questionText = field(question, "question", "Question") || `Question ${questionIndex + 1}`;
      const header = field(question, "header", "Header");
      const options = field(question, "options", "Options");
      const multiSelect = Boolean(field(question, "multiSelect", "MultiSelect"));
      const fieldset = document.createElement("fieldset");
      const legend = document.createElement("legend");
      legend.textContent = header ? `${header}: ${questionText}` : questionText;
      fieldset.appendChild(legend);

      (Array.isArray(options) ? options : []).forEach((option, optionIndex) => {
        const label = document.createElement("label");
        label.className = "choice";
        const input = document.createElement("input");
        input.type = multiSelect ? "checkbox" : "radio";
        input.name = `question-${questionIndex}`;
        input.value = String(field(option, "label", "Label") || "");
        input.dataset.questionIndex = String(questionIndex);
        label.appendChild(input);

        const copy = document.createElement("span");
        const optionLabel = field(option, "label", "Label") || "Option";
        const description = field(option, "description", "Description");
        copy.textContent = description ? `${optionLabel} — ${description}` : optionLabel;
        label.appendChild(copy);
        fieldset.appendChild(label);
      });

      const customLabel = document.createElement("label");
      customLabel.className = "custom-answer";
      customLabel.textContent = "Custom answer (optional)";
      const customInput = document.createElement("input");
      customInput.type = "text";
      customInput.name = `custom-${questionIndex}`;
      customInput.dataset.questionIndex = String(questionIndex);
      customInput.placeholder = "Add your own answer";
      customLabel.appendChild(customInput);
      fieldset.appendChild(customLabel);
      elements.questionItems.appendChild(fieldset);
    });
  }

  async function answerQuestion(event) {
    event.preventDefault();
    if (!pendingQuestion) {
      return;
    }

    const answers = {};
    const notes = {};
    let invalid = false;
    pendingQuestion.questions.forEach((question, questionIndex) => {
      const questionText = field(question, "question", "Question") || `Question ${questionIndex + 1}`;
      const inputs = Array.from(elements.questionItems.querySelectorAll(`input[data-question-index="${questionIndex}"]`));
      const selected = inputs.filter((input) => input.type === "radio" || input.type === "checkbox").filter((input) => input.checked).map((input) => input.value).filter(Boolean);
      const custom = inputs.find((input) => input.type === "text");
      const customText = custom ? custom.value.trim() : "";
      if (customText) {
        selected.push(customText);
        notes[questionText] = customText;
      }
      if (selected.length === 0) {
        invalid = true;
        return;
      }
      answers[questionText] = selected.join(", ");
    });

    if (invalid) {
      setQuestionError("Answer every question before sending.");
      return;
    }

    try {
      await requestJSON(`/questions/${encodeURIComponent(pendingQuestion.id)}/answer`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ answers, notes })
      });
      pendingQuestion = null;
      elements.question.hidden = true;
      setQuestionError("");
      setServerError("");
      await refreshStatus();
    } catch (error) {
      setQuestionError(error.message || "The answer could not be sent.");
      setServerError(error.message || "The answer could not be sent.");
    }
  }

  function connectEvents() {
    if (eventSource) {
      eventSource.close();
    }
    eventSource = new EventSource("/events");
    eventSource.addEventListener("open", () => setServerError(""));
    eventSource.addEventListener("stream_delta", (event) => {
      try {
        const payload = parseEvent(event);
        const text = field(payload, "text", "Text");
        if (typeof text === "string") {
          elements.stream.textContent += text;
        }
      } catch (error) {
        setServerError(error.message);
      }
    });
    eventSource.addEventListener("stream_clear", () => {
      elements.stream.textContent = "";
    });
    eventSource.addEventListener("host_event", (event) => {
      try {
        updateStatusFromEvent(parseEvent(event));
      } catch (error) {
        setServerError(error.message);
      }
    });
    eventSource.addEventListener("terminal", (event) => {
      try {
        updateStatusFromEvent(parseEvent(event));
      } catch (error) {
        setServerError(error.message);
      }
    });
    eventSource.addEventListener("question", handleQuestion);
    eventSource.addEventListener("reset", () => {
      void refreshStatus();
    });
    eventSource.addEventListener("error", () => {
      setServerError("The live event stream is disconnected; retrying…");
    });
  }

  function wireForm(formID, textareaID, action) {
    document.getElementById(formID).addEventListener("submit", (event) => {
      event.preventDefault();
      const textarea = document.getElementById(textareaID);
      void sendCommand(action, textarea.value);
    });
  }

  function start() {
    elements.serverError = document.getElementById("server-error");
    elements.statusText = document.getElementById("status-text");
    elements.stream = document.getElementById("stream");
    elements.question = document.getElementById("question");
    elements.questionItems = document.getElementById("question-items");
    elements.questionError = document.getElementById("question-error");

    wireForm("start-form", "start-text", "start");
    wireForm("steer-form", "steer-text", "steer");
    wireForm("continue-form", "continue-text", "continue");
    document.getElementById("pause-button").addEventListener("click", () => {
      void sendCommand("pause", "");
    });
    document.getElementById("reconnect-button").addEventListener("click", connectEvents);
    document.getElementById("question-form").addEventListener("submit", answerQuestion);

    void refreshStatus();
    connectEvents();
  }

  document.addEventListener("DOMContentLoaded", start);
})();
