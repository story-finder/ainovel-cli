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
    for (let index = start; index < value.length; index += 1) {
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
    for (let index = start; index < value.length; index += 1) {
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

  function parseLink(value, start) {
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
    const label = renderInline(value.slice(start + 1, labelEnd));
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

  function renderDelimited(value, start, marker, tag) {
    if (marker === "_" && start > 0 && /[\w]/.test(value[start - 1])) {
      return null;
    }
    const end = delimiterEnd(value, marker, start);
    if (end < 0) {
      return null;
    }
    return {
      html: `<${tag}>${renderInline(value.slice(start + marker.length, end))}</${tag}>`,
      end: end + marker.length,
    };
  }

  function renderCodeSpan(value, start) {
    let length = 1;
    while (value[start + length] === "`") {
      length += 1;
    }
    const marker = "`".repeat(length);
    const end = value.indexOf(marker, start + length);
    if (end < 0) {
      return null;
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

  function renderInline(value) {
    const input = String(value);
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
        if (code) {
          html += code.html;
          index = code.end - 1;
          continue;
        }
      }
      if (character === "[") {
        const link = parseLink(input, index);
        if (link) {
          html += link.html;
          index = link.end - 1;
          continue;
        }
      }
      if (input.startsWith("***", index) || input.startsWith("___", index)) {
        const marker = input.slice(index, index + 3);
        const delimited = renderDelimited(input, index, marker, "em");
        if (delimited) {
          const content = input.slice(index + 3, delimited.end - 3);
          html += `<strong><em>${renderInline(content)}</em></strong>`;
          index = delimited.end - 1;
          continue;
        }
      }
      if (input.startsWith("**", index) || input.startsWith("__", index)) {
        const marker = input.slice(index, index + 2);
        const delimited = renderDelimited(input, index, marker, "strong");
        if (delimited) {
          html += delimited.html;
          index = delimited.end - 1;
          continue;
        }
      }
      if (character === "*" || character === "_") {
        const delimited = renderDelimited(input, index, character, "em");
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

  function blockStart(line) {
    return Boolean(
      fenceStart(line) ||
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

      const heading = lines[index].match(/^ {0,3}(#{1,6})[ \t]+(.+?)\s*#*[ \t]*$/);
      if (heading) {
        const level = heading[1].length;
        const text = heading[2].replace(/[ \t]+#+[ \t]*$/, "");
        blocks.push(`<h${level}>${renderInline(text)}</h${level}>`);
        index += 1;
        continue;
      }

      if (/^ {0,3}>[ \t]?/.test(lines[index])) {
        const quote = [];
        while (index < lines.length && /^ {0,3}>[ \t]?/.test(lines[index])) {
          quote.push(lines[index].replace(/^ {0,3}>[ \t]?/, ""));
          index += 1;
        }
        blocks.push(`<blockquote>\n${renderBlocks(quote)}\n</blockquote>`);
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
      while (index < lines.length && lines[index].trim() && !blockStart(lines[index])) {
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

  return { escapeHTML, renderInto, renderMarkdown, sanitizeHref };
});
