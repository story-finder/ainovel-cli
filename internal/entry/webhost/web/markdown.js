(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) {
    module.exports = api;
  }
  root.AINovelMarkdown = api;
})(typeof globalThis === "object" ? globalThis : this, function () {
  "use strict";

  const HTML_ESCAPES = {
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  };
  const MAX_BLOCKQUOTE_DEPTH = 64;
  const MAX_INLINE_DEPTH = 64;
  const MAX_INLINE_SCAN = 2048;
  const MAX_CODE_SPAN_SCAN = 2048;
  const MAX_CODE_SPAN_MARKER = 16;

  function escapeHTML(value) {
    return String(value).replace(/[&<>"']/g, (character) => HTML_ESCAPES[character]);
  }

  function sanitizeHref(value) {
    const href = String(value).trim();
    if (!href) {
      return null;
    }

    const normalized = href
      .replace(/[\u0000-\u0020\u007f-\u009f]/g, "")
      .replace(/\\/g, "/")
      .toLowerCase();
    if (/^(?:javascript|data):/.test(normalized)) {
      return null;
    }
    if (/^[a-z][a-z\d+.-]*:/.test(normalized) && !/^(?:https?|mailto):/.test(normalized)) {
      return null;
    }
    if (normalized.startsWith("//")) {
      return null;
    }
    return href;
  }

  function findClosingBracket(value, start) {
    let depth = 0;
    const limit = Math.min(value.length, start + MAX_INLINE_SCAN);
    for (let index = start; index < limit; index += 1) {
      if (value[index] === "[") {
        depth += 1;
      } else if (value[index] === "]") {
        if (depth === 0) {
          return index;
        }
        depth -= 1;
      }
    }
    return -1;
  }

  function findClosingParenthesis(value, start) {
    let depth = 0;
    const limit = Math.min(value.length, start + MAX_INLINE_SCAN);
    for (let index = start; index < limit; index += 1) {
      if (value[index] === "(") {
        depth += 1;
      } else if (value[index] === ")") {
        depth -= 1;
        if (depth === 0) {
          return index;
        }
      }
    }
    return -1;
  }

  function linkDestination(value) {
    const destination = value.trim();
    if (destination.startsWith("<") && destination.endsWith(">")) {
      return destination.slice(1, -1);
    }
    const separator = destination.search(/[\s]/);
    return separator < 0 ? destination : destination.slice(0, separator);
  }

  function parseLink(value, start, depth) {
    const labelEnd = findClosingBracket(value, start + 1);
    if (labelEnd < 0 || value[labelEnd + 1] !== "(") {
      return null;
    }

    const close = findClosingParenthesis(value, labelEnd + 1);
    if (close < 0) {
      return null;
    }

    const destination = linkDestination(value.slice(labelEnd + 2, close));
    const href = sanitizeHref(destination);
    const label = renderInline(value.slice(start + 1, labelEnd), depth + 1);
    return {
      html: href === null ? label : `<a href="${escapeHTML(href)}">${label}</a>`,
      end: close + 1,
    };
  }

  function delimiterEnd(value, marker, start) {
    const end = value.indexOf(marker, start + marker.length);
    if (end <= start + marker.length || /^\s/.test(value.slice(start + marker.length, end))) {
      return -1;
    }
    return end;
  }

  function renderDelimited(value, start, marker, tag, depth) {
    if (marker === "_" && start > 0 && /[\w]/.test(value[start - 1])) {
      return null;
    }
    const end = delimiterEnd(value, marker, start);
    if (end < 0) {
      return null;
    }
    return {
      html: `<${tag}>${renderInline(value.slice(start + marker.length, end), depth + 1)}</${tag}>`,
      end: end + marker.length,
    };
  }

  function findCodeSpanEnd(value, marker, start) {
    const limit = Math.min(value.length - marker.length, start + MAX_CODE_SPAN_SCAN);
    for (let index = start; index <= limit; index += 1) {
      if (value.startsWith(marker, index)) {
        return index;
      }
    }
    return -1;
  }

  function renderCodeSpan(value, start) {
    let runEnd = start + 1;
    while (value[runEnd] === "`") {
      runEnd += 1;
    }
    const length = runEnd - start;
    if (length > MAX_CODE_SPAN_MARKER) {
      return {
        html: escapeHTML(value.slice(start, runEnd)),
        end: runEnd,
      };
    }

    const marker = "`".repeat(length);
    const end = findCodeSpanEnd(value, marker, runEnd);
    if (end < 0) {
      const fallbackEnd = Math.min(value.length, start + MAX_CODE_SPAN_SCAN);
      return {
        html: escapeHTML(value.slice(start, fallbackEnd)),
        end: fallbackEnd,
      };
    }

    let code = value.slice(start + length, end).replace(/\n/g, " ");
    if (code.length > 1 && code.startsWith(" ") && code.endsWith(" ") && /\S/.test(code)) {
      code = code.slice(1, -1);
    }
    return {
      html: `<code>${escapeHTML(code)}</code>`,
      end: end + length,
    };
  }

  function renderInline(value, depth = 0) {
    const input = String(value);
    if (depth >= MAX_INLINE_DEPTH) {
      return escapeHTML(input);
    }
    let html = "";
    for (let index = 0; index < input.length; index += 1) {
      const character = input[index];

      if (character === "\\" && input[index + 1] === "\n") {
        html += "<br>\n";
        index += 1;
        continue;
      }
      if (character === "\\" && "\\`*_{}[]()#+-.!".includes(input[index + 1])) {
        html += escapeHTML(input[index + 1]);
        index += 1;
        continue;
      }
      if (character === "\n") {
        if (/ +$/.test(html)) {
          html = html.replace(/ +$/, "");
          html += "<br>\n";
        } else {
          html += "\n";
        }
        continue;
      }
      if (character === "`") {
        const code = renderCodeSpan(input, index);
        html += code.html;
        index = code.end - 1;
        continue;
      }
      if (character === "[") {
        const link = parseLink(input, index, depth);
        if (link) {
          html += link.html;
          index = link.end - 1;
          continue;
        }
      }
      if (input.startsWith("***", index) || input.startsWith("___", index)) {
        const marker = input.slice(index, index + 3);
        const delimited = renderDelimited(input, index, marker, "em", depth);
        if (delimited) {
          html += `<strong>${delimited.html}</strong>`;
          index = delimited.end - 1;
          continue;
        }
      }
      if (input.startsWith("**", index) || input.startsWith("__", index)) {
        const marker = input.slice(index, index + 2);
        const delimited = renderDelimited(input, index, marker, "strong", depth);
        if (delimited) {
          html += delimited.html;
          index = delimited.end - 1;
          continue;
        }
      }
      if (character === "*" || character === "_") {
        const delimited = renderDelimited(input, index, character, "em", depth);
        if (delimited) {
          html += delimited.html;
          index = delimited.end - 1;
          continue;
        }
      }
      html += escapeHTML(character);
    }
    return html;
  }

  function fenceStart(line) {
    return line.match(/^ {0,3}(`{3,}|~{3,})(.*)$/);
  }

  function fenceClose(line, marker) {
    const escapedMarker = marker[0] === "`" ? "`" : "~";
    const expression = new RegExp(`^ {0,3}${escapedMarker}{${marker.length},}\\s*$`);
    return expression.test(line);
  }

  function stripBlockquotePrefix(line) {
    const match = line.match(/^((?: {0,3}>[ \t]?)+)/);
    if (!match) {
      return { depth: 0, text: line };
    }
    return {
      depth: Math.min(MAX_BLOCKQUOTE_DEPTH, (match[1].match(/>/g) || []).length),
      text: line.slice(match[1].length),
    };
  }

  function renderBlockquote(lines, start) {
    const content = [];
    let depth = 1;
    let index = start;
    while (index < lines.length && /^ {0,3}>[ \t]?/.test(lines[index])) {
      const stripped = stripBlockquotePrefix(lines[index]);
      depth = Math.max(depth, stripped.depth);
      content.push(stripped.text);
      index += 1;
    }

    return {
      html: `${"<blockquote>\n".repeat(depth)}${renderBlocks(content)}${"\n</blockquote>".repeat(depth)}`,
      end: index,
    };
  }

  function isHorizontalRule(line) {
    const value = line.trim();
    return (
      /^(?:\*[ \t]*){3,}$/.test(value) ||
      /^(?:-[ \t]*){3,}$/.test(value) ||
      /^(?:_[ \t]*){3,}$/.test(value)
    );
  }

  function splitTableRow(line) {
    let value = line.trim();
    if (value.startsWith("|")) {
      value = value.slice(1);
    }
    if (value.endsWith("|") && !value.endsWith("\\|")) {
      value = value.slice(0, -1);
    }
    return value.split("|").map((cell) => cell.trim());
  }

  function isTableSeparator(line) {
    const cells = splitTableRow(line);
    return cells.length > 1 && cells.every((cell) => /^:?-{3,}:?$/.test(cell));
  }

  function isTableStart(lines, index) {
    return (
      index + 1 < lines.length &&
      lines[index].includes("|") &&
      splitTableRow(lines[index]).length > 1 &&
      isTableSeparator(lines[index + 1])
    );
  }

  function renderTableCells(cells, tag, count) {
    const output = [];
    for (let index = 0; index < count; index += 1) {
      output.push(`<${tag}>${renderInline(cells[index] || "")}</${tag}>`);
    }
    return output.join("");
  }

  function renderTable(lines, start) {
    const headers = splitTableRow(lines[start]);
    const rows = [];
    let index = start + 2;
    while (index < lines.length && lines[index].trim() && lines[index].includes("|")) {
      const cells = splitTableRow(lines[index]);
      if (cells.length < 2) {
        break;
      }
      rows.push(`<tr>${renderTableCells(cells, "td", headers.length)}</tr>`);
      index += 1;
    }

    const body = rows.join("\n");
    return {
      html: `<table>\n<thead>\n<tr>${renderTableCells(headers, "th", headers.length)}</tr>\n</thead>\n<tbody>\n${body}\n</tbody>\n</table>`,
      end: index,
    };
  }

  function blockStart(line) {
    return Boolean(
      fenceStart(line) ||
        isHorizontalRule(line) ||
        /^ {0,3}#{1,6}[ \t]+/.test(line) ||
        /^ {0,3}>[ \t]?/.test(line) ||
        /^ {0,3}[-+*][ \t]+/.test(line) ||
        /^ {0,3}\d+[.)][ \t]+/.test(line),
    );
  }

  function renderList(lines, start, ordered) {
    const items = [];
    let index = start;
    const pattern = ordered ? /^ {0,3}\d+[.)][ \t]+(.+)$/ : /^ {0,3}[-+*][ \t]+(.+)$/;
    while (index < lines.length) {
      const match = lines[index].match(pattern);
      if (!match) {
        break;
      }
      items.push(`<li>${renderInline(match[1])}</li>`);
      index += 1;
    }
    const tag = ordered ? "ol" : "ul";
    return {
      html: `<${tag}>\n${items.join("\n")}\n</${tag}>`,
      end: index,
    };
  }

  function renderBlocks(lines) {
    const blocks = [];
    let index = 0;
    while (index < lines.length) {
      if (!lines[index].trim()) {
        index += 1;
        continue;
      }

      const fence = fenceStart(lines[index]);
      if (fence) {
        const marker = fence[1];
        const code = [];
        const info = fence[2].trim().split(/[\s]/)[0];
        index += 1;
        while (index < lines.length && !fenceClose(lines[index], marker)) {
          code.push(lines[index]);
          index += 1;
        }
        if (index < lines.length) {
          index += 1;
        }
        const language = /^[A-Za-z0-9_-]+$/.test(info) ? ` class="language-${escapeHTML(info)}"` : "";
        blocks.push(`<pre><code${language}>${escapeHTML(code.join("\n"))}</code></pre>`);
        continue;
      }

      if (isHorizontalRule(lines[index])) {
        blocks.push("<hr>");
        index += 1;
        continue;
      }

      if (isTableStart(lines, index)) {
        const table = renderTable(lines, index);
        blocks.push(table.html);
        index = table.end;
        continue;
      }

      const heading = lines[index].match(/^ {0,3}(#{1,6})[ \t]+(.+?)\s*#*[ \t]*$/);
      if (heading) {
        const level = heading[1].length;
        const text = heading[2].replace(/[ \t]+#+[ \t]*$/, "");
        blocks.push(`<h${level}>${renderInline(text)}</h${level}>`);
        index += 1;
        continue;
      }

      if (/^ {0,3}>[ \t]?/.test(lines[index])) {
        const quote = renderBlockquote(lines, index);
        blocks.push(quote.html);
        index = quote.end;
        continue;
      }

      const unordered = lines[index].match(/^ {0,3}[-+*][ \t]+/);
      const ordered = lines[index].match(/^ {0,3}\d+[.)][ \t]+/);
      if (unordered || ordered) {
        const list = renderList(lines, index, Boolean(ordered));
        blocks.push(list.html);
        index = list.end;
        continue;
      }

      const paragraph = [];
      while (
        index < lines.length &&
        lines[index].trim() &&
        !blockStart(lines[index]) &&
        !isTableStart(lines, index)
      ) {
        paragraph.push(lines[index]);
        index += 1;
      }
      if (paragraph.length === 0) {
        paragraph.push(lines[index]);
        index += 1;
      }
      blocks.push(`<p>${renderInline(paragraph.join("\n"))}</p>`);
    }
    return blocks.join("\n");
  }

  function renderMarkdown(value) {
    const source = value === null || value === undefined ? "" : String(value);
    return renderBlocks(source.replace(/\r\n?/g, "\n").split("\n"));
  }

  function renderInto(container, markdown) {
    if (!container || typeof container !== "object") {
      throw new TypeError("renderInto requires a DOM container");
    }
    container.innerHTML = renderMarkdown(markdown);
    return container;
  }

  return { escapeHTML, render: renderMarkdown, renderInto, renderMarkdown, sanitizeHref };
});
