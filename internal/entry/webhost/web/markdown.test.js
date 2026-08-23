// Run with: node --test internal/entry/webhost/web/markdown.test.js

const assert = require("node:assert/strict");
const { performance } = require("node:perf_hooks");
const { test } = require("node:test");

const { render, renderMarkdown } = require("./markdown.js");

test("exports render as a compatible alias", () => {
  assert.strictEqual(render, renderMarkdown);
  assert.equal(render("# Alias"), "<h1>Alias</h1>");
});

test("renders common Markdown blocks and inline styles", () => {
  const source = [
    "# Heading",
    "",
    "Paragraph with **strong**, *emphasis*, `code`, and [link](https://example.com).",
    "line  ",
    "break",
    "",
    "- first",
    "- second",
    "",
    "1. one",
    "2. two",
    "",
    "> quote",
    "",
    "```js",
    'const value = "<unsafe>";',
    "```",
  ].join("\n");

  const html = renderMarkdown(source);

  assert.match(html, /<h1>Heading<\/h1>/);
  assert.match(html, /<strong>strong<\/strong>/);
  assert.match(html, /<em>emphasis<\/em>/);
  assert.match(html, /<code>code<\/code>/);
  assert.match(html, /<a href="https:\/\/example\.com">link<\/a>/);
  assert.match(html, /line<br>\nbreak/);
  assert.match(html, /<ul>\n<li>first<\/li>\n<li>second<\/li>\n<\/ul>/);
  assert.match(html, /<ol>\n<li>one<\/li>\n<li>two<\/li>\n<\/ol>/);
  assert.match(html, /<blockquote>\n<p>quote<\/p>\n<\/blockquote>/);
  assert.match(html, /<pre><code class="language-js">const value = &quot;&lt;unsafe&gt;&quot;;<\/code><\/pre>/);
});

test("renders horizontal rules and simple tables", () => {
  const html = renderMarkdown(
    [
      "---",
      "",
      "| Name | Value |",
      "| --- | --- |",
      "| Alice | **one** |",
      "| Bob | `two` |",
    ].join("\n"),
  );

  assert.match(html, /<hr>\n/);
  assert.match(html, /<table>\n<thead>\n<tr><th>Name<\/th><th>Value<\/th><\/tr>\n<\/thead>/);
  assert.match(html, /<tbody>\n<tr><td>Alice<\/td><td><strong>one<\/strong><\/td><\/tr>/);
  assert.match(html, /<tr><td>Bob<\/td><td><code>two<\/code><\/td><\/tr>\n<\/tbody>/);
});

test("escapes raw HTML and omits unsafe link destinations", () => {
  const html = renderMarkdown(
    'raw <img src=x onerror="alert(1)"> [bad](javascript:alert(1)) [data](data:text/html,pwn) [safe](mailto:user@example.com) [relative](../chapter)',
  );

  assert.match(html, /&lt;img src=x onerror=&quot;alert\(1\)&quot;&gt;/);
  assert.doesNotMatch(html, /<img|<script/i);
  assert.doesNotMatch(html, /href="(?:javascript|data):/i);
  assert.match(html, /bad/);
  assert.match(html, /data/);
  assert.match(html, /<a href="mailto:user@example\.com">safe<\/a>/);
  assert.match(html, /<a href="\.\.\/chapter">relative<\/a>/);
});

test("does not overflow on deeply nested blockquote markers", () => {
  const html = renderMarkdown(`${">".repeat(10000)} text`);

  assert.match(html, /text/);
  assert.ok((html.match(/<blockquote>/g) || []).length <= 64);
});

test("does not overflow on deeply nested links", () => {
  let nested = "text";
  for (let index = 0; index < 5000; index += 1) {
    nested = `[${nested}](u)`;
  }

  assert.doesNotThrow(() => renderMarkdown(nested));

  nested = "text";
  for (let index = 0; index < 100; index += 1) {
    nested = `[${nested}](u)`;
  }
  const html = renderMarkdown(nested);
  assert.ok((html.match(/<a href="u">/g) || []).length <= 64);
});

test("falls back safely when link bracket scans exceed the inline limit", () => {
  const source = `[${"x".repeat(5000)}](https://example.com)`;
  const html = renderMarkdown(source);

  assert.doesNotMatch(html, /<a href=/);
  assert.ok(html.includes(source));
});

test("bounds unmatched code span scans", { timeout: 3000 }, () => {
  const source = `x${"`".repeat(40000)}`;
  const started = performance.now();
  const html = renderMarkdown(source);
  const elapsed = performance.now() - started;

  assert.ok(elapsed < 1000, `render took ${elapsed.toFixed(1)}ms`);
  assert.equal(html, `<p>${source}</p>`);
});
