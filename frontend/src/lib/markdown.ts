// Koyori IDE 模块 · Markdown。
// 喵，这是 Koyori IDE 的 Markdown 模块（前端实现）~
import { marked } from "marked";
import DOMPurify from "dompurify";
import hljs from "highlight.js/lib/common";
// 额外注册 common 包未包含但常用的语言，覆盖更多开发场景。
import tsx from "highlight.js/lib/languages/typescript";
import jsx from "highlight.js/lib/languages/javascript";
import dockerfile from "highlight.js/lib/languages/dockerfile";
import nginx from "highlight.js/lib/languages/nginx";
import protobuf from "highlight.js/lib/languages/protobuf";
import scala from "highlight.js/lib/languages/scala";
import groovy from "highlight.js/lib/languages/groovy";
import dart from "highlight.js/lib/languages/dart";
import elixir from "highlight.js/lib/languages/elixir";
import haskell from "highlight.js/lib/languages/haskell";
import clojure from "highlight.js/lib/languages/clojure";
import vim from "highlight.js/lib/languages/vim";
import powershell from "highlight.js/lib/languages/powershell";
import ocaml from "highlight.js/lib/languages/ocaml";
import erlang from "highlight.js/lib/languages/erlang";
import { translate, getCurrentLocale } from "@/lib/i18n";

// Register extra languages beyond the common set.
hljs.registerLanguage("tsx", tsx);
hljs.registerLanguage("jsx", jsx);
hljs.registerLanguage("dockerfile", dockerfile);
hljs.registerLanguage("nginx", nginx);
hljs.registerLanguage("protobuf", protobuf);
hljs.registerLanguage("scala", scala);
hljs.registerLanguage("groovy", groovy);
hljs.registerLanguage("dart", dart);
hljs.registerLanguage("elixir", elixir);
hljs.registerLanguage("haskell", haskell);
hljs.registerLanguage("clojure", clojure);
hljs.registerLanguage("vim", vim);
hljs.registerLanguage("powershell", powershell);
hljs.registerLanguage("ocaml", ocaml);
hljs.registerLanguage("erlang", erlang);

// Markdown 代码围栏中常用的别名 → hljs 注册名映射。
// marked 会把 ```ts 翻译成 class="language-ts"，但 hljs 注册名是
// "typescript"。"aliases" 字段 hljs 内部已注册部分，但为了兼容更多
// 简写（如 golang/py/sh），这里显式补全映射。
const LANG_ALIASES: Record<string, string> = {
  ts: "typescript",
  tsx: "tsx",
  js: "javascript",
  jsx: "jsx",
  mjs: "javascript",
  cjs: "javascript",
  es: "javascript",
  es6: "javascript",
  go: "go",
  golang: "go",
  py: "python",
  python3: "python",
  rb: "ruby",
  rs: "rust",
  sh: "bash",
  bash: "bash",
  zsh: "bash",
  shell: "shell",
  ps: "powershell",
  ps1: "powershell",
  pwsh: "powershell",
  yml: "yaml",
  md: "markdown",
  markdown: "markdown",
  cplusplus: "cpp",
  cc: "cpp",
  h: "cpp",
  hpp: "cpp",
  cs: "csharp",
  fs: "fsharp",
  fsharp: "fsharp",
  kt: "kotlin",
  kts: "kotlin",
  scala: "scala",
  sc: "scala",
  groovy: "groovy",
  gradle: "groovy",
  dart: "dart",
  elixir: "elixir",
  ex: "elixir",
  exs: "elixir",
  hs: "haskell",
  clj: "clojure",
  cljs: "clojure",
  edn: "clojure",
  ml: "ocaml",
  erl: "erlang",
  vim: "vim",
  viml: "vim",
  dockerfile: "dockerfile",
  docker: "dockerfile",
  proto: "protobuf",
  protobuf: "protobuf",
  nginx: "nginx",
  conf: "nginx",
  ini: "ini",
  toml: "ini",
  tex: "latex",
  latex: "latex",
  html: "xml",
  xml: "xml",
  svg: "xml",
  rss: "xml",
  plist: "xml",
  sql: "sql",
  mysql: "sql",
  postgres: "sql",
  postgresql: "sql",
  psql: "sql",
  graphql: "graphql",
  gql: "graphql",
  wasm: "wasm",
  wat: "wasm",
  lua: "lua",
  make: "makefile",
  makefile: "makefile",
  cmake: "makefile",
  diff: "diff",
  patch: "diff",
  plaintext: "plaintext",
  text: "plaintext",
  txt: "plaintext",
  log: "plaintext",
};

function resolveLanguage(lang: string): string {
  const lower = lang.toLowerCase();
  return LANG_ALIASES[lower] ?? lower;
}

// Configure marked once
marked.setOptions({
  gfm: true,
  breaks: false,
});

/**
 * Applies highlight.js syntax highlighting to all `<pre><code>` blocks in the
 * HTML string. Uses the language-XXX class produced by marked to pick the
 * language; falls back to auto-detection when no class is present or the
 * language is unknown. Returns the HTML with highlighted code blocks.
 *
 * Inline `<code>` (not inside `<pre>`) is left untouched.
 */
function highlightCodeBlocks(html: string): string {
  const parser = new DOMParser();
  const doc = parser.parseFromString(html, "text/html");
  const codeBlocks = doc.querySelectorAll("pre > code");
  if (codeBlocks.length === 0) return html;
  codeBlocks.forEach((codeEl) => {
    const langMatch = codeEl.className.match(/language-([\w-]+)/);
    const rawLang = langMatch?.[1] || "";
    const lang = rawLang ? resolveLanguage(rawLang) : "";
    const code = codeEl.textContent || "";
    let highlighted: string;
    try {
      if (lang && hljs.getLanguage(lang)) {
        highlighted = hljs.highlight(code, { language: lang }).value;
      } else {
        highlighted = hljs.highlightAuto(code).value;
      }
    } catch {
      highlighted = code;
    }
    codeEl.innerHTML = highlighted;
    codeEl.classList.add("hljs");
    // 同步更新语言标签为解析后的注册名，便于 CSS 按语言定制样式。
    if (rawLang && rawLang !== lang) {
      codeEl.classList.remove(`language-${rawLang}`);
      codeEl.classList.add(`language-${lang}`);
    }
  });
  return doc.body.innerHTML;
}

/**
 * M-21: DOMPurify hook registration guard. The afterSanitizeAttributes hook
 * (forces external links to open in a new tab with noopener/noreferrer) is
 * identical for every sanitizeHtml / sanitizeMarketplaceHtml call. Previously
 * each call did addHook + removeHook, which (a) is wasteful and (b) can leak
 * hooks if removeHook is skipped. Now the hook is registered ONCE at module
 * load and never removed; subsequent sanitize calls skip registration.
 */
let domPurifyHookRegistered = false;

function ensureDomPurifyHook(): void {
  if (domPurifyHookRegistered) return;
  DOMPurify.addHook("afterSanitizeAttributes", (node) => {
    if (node.tagName === "A" && node.getAttribute("href")) {
      node.setAttribute("target", "_blank");
      node.setAttribute("rel", "noopener noreferrer");
    }
  });
  domPurifyHookRegistered = true;
}

// Register once at module load.
ensureDomPurifyHook();

/**
 * Sanitizes HTML to prevent XSS using DOMPurify.
 *
 * H-16: 加强 DOMPurify 配置 ——
 *   - 显式禁用 `style` 属性（CSS 注入：position/fixed、background-image 等
 *     可被用于点击劫持或外部资源加载）。
 *   - 移除 `id` 属性白名单（DOM clobbering：攻击者可命名元素 id 为
 *     `__proto__` / 表单字段名等覆盖全局引用）。
 *   - 其余属性白名单保留不变（href/title/class/src/alt/target/rel）。
 */
export function sanitizeHtml(html: string): string {
  // M-21: Ensure the afterSanitizeAttributes hook is registered. The guard
  // inside ensureDomPurifyHook makes this a no-op after the first call
  // (the hook is also registered at module load, so this is belt-and-
  // suspenders for environments where module-load side effects are deferred).
  ensureDomPurifyHook();
  const result = DOMPurify.sanitize(html, {
    ALLOWED_TAGS: [
      "h1", "h2", "h3", "h4", "h5", "h6",
      "p", "br", "hr", "blockquote", "pre", "code",
      "ul", "ol", "li", "dl", "dt", "dd",
      "table", "thead", "tbody", "tr", "th", "td",
      "a", "strong", "em", "del", "ins", "sub", "sup",
      "span", "div", "img", "details", "summary",
    ],
    // H-16: 不再允许 `id`（DOM clobbering）和 `style`（CSS 注入）。
    // 显式声明 FORBID_ATTR 作为纵深防御：即使未来误加入白名单也会被拦截。
    ALLOWED_ATTR: ["href", "title", "class", "src", "alt", "target", "rel"],
    FORBID_ATTR: ["style", "id"],
    ALLOW_DATA_ATTR: false,
  });
  return result;
}

/**
 * H-16: 第三方扩展市场内容（README/CHANGELOG）使用更严格的白名单。
 *
 * 与 {@link sanitizeHtml} 不同，本函数：
 *   - 只允许基本格式标签（p, h1-h6, ul, ol, li, code, pre, a, img,
 *     strong, em, br, table, thead, tbody, tr, td, th）。
 *   - 不允许 div / span / blockquote / details / hr / del / ins / sub /
 *     sup / dl / dt / dd 等容器或装饰标签。
 *   - 同样禁用 `id` 和 `style` 属性。
 *
 * 这样即使第三方扩展的 README 含恶意 HTML（如 `<img onerror>`、
 * `<div id="x">` 用于 DOM clobbering），也会被剥离为安全子集。
 */
export function sanitizeMarketplaceHtml(html: string): string {
  // M-21: Ensure the afterSanitizeAttributes hook is registered (guarded).
  ensureDomPurifyHook();
  const result = DOMPurify.sanitize(html, {
    ALLOWED_TAGS: [
      "p", "br",
      "h1", "h2", "h3", "h4", "h5", "h6",
      "ul", "ol", "li",
      "code", "pre",
      "a", "img",
      "strong", "em",
      "table", "thead", "tbody", "tr", "td", "th",
    ],
    ALLOWED_ATTR: ["href", "title", "src", "alt", "target", "rel"],
    FORBID_ATTR: ["style", "id", "class"],
    ALLOW_DATA_ATTR: false,
  });
  return result;
}

/**
 * M-21: Test-only helper. Removes the registered afterSanitizeAttributes
 * hook and resets the guard so a test can re-trigger registration and spy
 * on DOMPurify.addHook to verify it is called at most once.
 */
export function __resetDomPurifyHookForTesting(): void {
  DOMPurify.removeHook("afterSanitizeAttributes");
  domPurifyHookRegistered = false;
}

/**
 * Renders markdown to sanitized HTML with syntax-highlighted code blocks.
 * highlight.js is applied to fenced code blocks before sanitization; the
 * `<span class="hljs-*">` tags it produces survive DOMPurify because `span`
 * and `class` are in the allow-list.
 *
 * Results are memoized in an LRU cache keyed by the raw markdown string, so
 * repeated renders of the same message (e.g. on every Vue re-render) return
 * the cached HTML without re-parsing / re-highlighting / re-sanitizing. This
 * is the main fix for AI page performance: a long conversation used to re-run
 * marked + hljs + DOMPurify for every message on every keystroke.
 */
export function renderMarkdown(md: string): string {
  if (!md) return "";
  const cached = renderCache.get(md);
  if (cached !== undefined) {
    // Map.get does not refresh insertion order; re-insert to mark as recently
    // used so the LRU eviction picks the actual least-recently-used entry.
    renderCache.delete(md);
    renderCache.set(md, cached);
    return cached;
  }
  const rawHtml = marked.parse(md, { async: false }) as string;
  const highlighted = highlightCodeBlocks(rawHtml);
  const html = sanitizeHtml(highlighted);
  putLru(renderCache, md, html);
  return html;
}

/**
 * H-16: 渲染第三方扩展市场的 README/CHANGELOG markdown，使用更严格的
 * 白名单（{@link sanitizeMarketplaceHtml}）。
 *
 * 与 {@link renderMarkdown} 不同：
 *   - 仍然经过 highlight.js 高亮（span 会被 sanitizer 剥离，但代码文本
 *     保留在 `<pre><code>` 内）。
 *   - 使用 {@link sanitizeMarketplaceHtml} 而非 {@link sanitizeHtml}，
 *     只允许基本格式标签，禁用 id/style/class。
 *
 * 结果缓存在独立的 LRU 中（与 renderMarkdown 的缓存隔离，因输出不同）。
 */
export function renderMarketplaceMarkdown(md: string): string {
  if (!md) return "";
  const cached = marketplaceCache.get(md);
  if (cached !== undefined) {
    marketplaceCache.delete(md);
    marketplaceCache.set(md, cached);
    return cached;
  }
  const rawHtml = marked.parse(md, { async: false }) as string;
  const highlighted = highlightCodeBlocks(rawHtml);
  const html = sanitizeMarketplaceHtml(highlighted);
  putLru(marketplaceCache, md, html);
  return html;
}

/**
 * Renders markdown to sanitized HTML, then wraps each `<pre>` block in a
 * `.code-block-wrap` container with an "Apply" button (`code-block-apply-btn`).
 *
 * The button carries a `data-code-index` attribute matching the order of
 * appearance (0-based), so consumers can map clicks back to source content if
 * needed. The code itself can also be re-extracted from the `<pre>`'s
 * `textContent` at click time.
 *
 * Safe because it post-processes the already-sanitized HTML using DOMParser;
 * no raw user input is injected outside of `<pre>`.
 *
 * Results are memoized in a separate LRU cache (the output differs from
 * `renderMarkdown` because of the Apply-button wrappers).
 */
export function renderMarkdownWithApplyButtons(md: string): string {
  if (!md) return "";
  // The Apply button labels come from i18n, so the locale is part of the
  // cache key — changing the language must not serve stale button text.
  const cacheKey = `${getCurrentLocale()}\u0000${md}`;
  const cached = applyButtonsCache.get(cacheKey);
  if (cached !== undefined) {
    applyButtonsCache.delete(cacheKey);
    applyButtonsCache.set(cacheKey, cached);
    return cached;
  }
  const sanitized = renderMarkdown(md);
  const parser = new DOMParser();
  const doc = parser.parseFromString(sanitized, "text/html");
  const pres = doc.querySelectorAll("pre");
  pres.forEach((pre, idx) => {
    const wrap = doc.createElement("div");
    wrap.className = "code-block-wrap";
    const btn = doc.createElement("button");
    btn.className = "code-block-apply-btn";
    btn.type = "button";
    btn.setAttribute("aria-label", translate("markdown.applyButtonAria"));
    btn.setAttribute("title", translate("markdown.applyButtonTitle"));
    btn.setAttribute("data-code-index", String(idx));
    btn.textContent = translate("markdown.applyButton");
    pre.parentNode?.insertBefore(wrap, pre);
    wrap.appendChild(pre);
    wrap.appendChild(btn);
  });
  const html = doc.body.innerHTML;
  putLru(applyButtonsCache, cacheKey, html);
  return html;
}

// --- LRU cache infrastructure ------------------------------------------------

const MARKDOWN_CACHE_LIMIT = 100;

const renderCache = new Map<string, string>();
const applyButtonsCache = new Map<string, string>();
// H-16: 市场内容（第三方 README）专用缓存，与 renderCache 隔离（白名单不同）
const marketplaceCache = new Map<string, string>();

/**
 * Inserts (or updates) a key/value pair in the LRU map, evicting the
 * least-recently-used entry (the first key in insertion order) when the
 * capacity is exceeded. Map preserves insertion order in JS, so the oldest
 * entry is `map.keys().next().value`.
 *
 * M-23: 导出供 DiffViewer.vue 的行级高亮缓存复用（相同的 LRU 驱逐策略）。
 */
export function putLru(cache: Map<string, string>, key: string, value: string): void {
  if (cache.size >= MARKDOWN_CACHE_LIMIT) {
    const oldest = cache.keys().next().value;
    if (oldest !== undefined) cache.delete(oldest);
  }
  cache.set(key, value);
}

/**
 * Clears the markdown render caches. Intended for tests and for forcing a
 * re-render after locale changes (the Apply button label is i18n-sensitive).
 */
export function clearMarkdownCache(): void {
  renderCache.clear();
  applyButtonsCache.clear();
  marketplaceCache.clear();
}

// --- H-17: Artifact 预览 iframe 沙箱 ------------------------------------------------

/**
 * H-17: Artifact 预览 iframe 的 Content-Security-Policy。
 *
 * 设计意图：iframe 已通过 `sandbox="allow-scripts"`（不含
 * allow-same-origin / allow-popups / allow-forms）限制了脚本的同源访问、
 * 弹窗与表单提交。但 sandbox 仍允许脚本发起网络请求（fetch/XHR），
 * 因此通过 CSP 进一步收紧：
 *   - default-src 'none'         默认禁止所有资源加载
 *   - script-src 'nonce-…'       只允许本次 srcdoc 标记过的脚本
 *   - style-src 'unsafe-inline'  允许内联样式
 *   - img-src data: https:       允许 data URI 与 HTTPS 图片
 *   - font-src data:             允许 data URI 字体
 *   - connect-src 'none'         禁止任何网络请求（防数据外泄到攻击者服务器）
 *   - form-action 'none'         禁止表单提交（纵深防御，sandbox 已禁）
 *   - base-uri 'none'            禁止 <base> 标签篡改相对 URL
 *   - frame-src 'none'           禁止嵌套 iframe
 */
function createArtifactNonce(): string {
  const cryptoApi = globalThis.crypto;
  if (!cryptoApi?.getRandomValues) {
    throw new Error("Secure random generation is unavailable for artifact CSP nonce");
  }
  const bytes = cryptoApi.getRandomValues(new Uint8Array(16));
  return Array.from(bytes, (value) => value.toString(16).padStart(2, "0")).join("");
}

function artifactCsp(nonce: string): string {
  return (
    "default-src 'none'; " +
    `script-src 'nonce-${nonce}'; ` +
    "style-src 'unsafe-inline'; " +
    "img-src data: https:; " +
    "font-src data:; " +
    "connect-src 'none'; " +
    "form-action 'none'; " +
    "base-uri 'none'; " +
    "frame-src 'none'"
  );
}

/**
 * H-17: 从 SVG 内容中剥离 `<script>` 标签。
 *
 * SVG 中的 `<script>` 在被浏览器渲染为 SVG 时会执行（即使外层是
 * `text/html` 文档）。CSP 的 `script-src` 会限制脚本执行，但为了纵深
 * 防御，且避免脚本在 CSP 应用前运行（某些浏览器实现细节），这里显式
 * 移除所有 `<script>` 元素，包括：
 *   - 成对形式：`<script>...</script>` 或 `<script type="...">...</script>`
 *   - SVG 自闭合形式：`<script href="..." />`（SVG 1.1 特有语法）
 */
function stripSvgScripts(svg: string): string {
  return svg
    .replace(/<script[\s\S]*?<\/script>/gi, "")
    .replace(/<script\b[^>]*\/>/gi, "");
}

/**
 * H-17: 构建 Artifact 预览 iframe 的 srcdoc，注入严格 CSP。
 *
 * @param content AI 生成的 HTML 或 SVG 内容
 * @param isSvg   是否为 SVG 内容（SVG 会额外剥离 `<script>` 标签）
 * @returns 可用于 iframe `srcdoc` 的完整 HTML 文档字符串
 *
 * 安全策略：
 *   1. iframe `sandbox="allow-scripts"`（由 DiffViewer.vue 设置）——
 *      脚本运行在 null origin，无法访问父窗口 / 同源存储 / 弹窗 / 表单。
 *   2. CSP `connect-src 'none'`——禁止 fetch/XHR/WebSocket 数据外泄。
 *   3. SVG `<script>` 标签被显式剥离（纵深防御）。
 *
 * 若内容已是完整 HTML 文档（含 `<head>`），CSP meta 注入到现有 `<head>`
 * 开头；否则包装为新的完整 HTML 文档。
 */
export function buildArtifactSrcDoc(content: string, isSvg: boolean): string {
  let processed = content;
  if (isSvg) {
    processed = stripSvgScripts(processed);
  }
  const nonce = createArtifactNonce();
  const parser = new DOMParser();
  const doc = parser.parseFromString(processed, "text/html");

  doc.head.querySelectorAll("meta[http-equiv]").forEach((meta) => {
    if (meta.getAttribute("http-equiv")?.toLocaleLowerCase() === "content-security-policy") {
      meta.remove();
    }
  });

  const cspMeta = doc.createElement("meta");
  cspMeta.setAttribute("http-equiv", "Content-Security-Policy");
  cspMeta.setAttribute("content", artifactCsp(nonce));
  doc.head.prepend(cspMeta);

  // DOMParser does not execute scripts. Mark every artifact script with the
  // one-time nonce so interactive previews do not require unsafe-inline.
  doc.querySelectorAll("script").forEach((script) => {
    script.setAttribute("nonce", nonce);
  });

  return `<!DOCTYPE html>${doc.documentElement.outerHTML}`;
}
