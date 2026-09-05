#!/usr/bin/env node

// Bindings import layering guard (P19 P1-07).
//
// Decision (written here per prompt-19 P1-07 "先做决策并写明"):
//
//   The generated `frontend/bindings/**` tree is consumed through the
//   `frontend/src/api/*` wrapper layer. Converging the ~15 existing direct
//   consumers into `api/` wrappers would be a pure mechanical move with no
//   error-handling gain — exactly what P1-07 forbids ("不做纯机械大搬移，
//   优先保证错误处理一致性"). Instead the bypasses are registered as
//   sanctioned exceptions and this guard turns the layering into an
//   enforced contract: any NEW direct bindings import outside the
//   sanctioned categories fails CI and must either go through `src/api/*`
//   or be added to the registry below with a reason.
//
// Sanctioned categories (in order):
//   1. `frontend/src/api/**`      — the wrapper layer itself.
//   2. `frontend/src/**/*.test.ts` — tests pinning the generated surface.
//   3. `frontend/src/e2e/**`      — runtime probes that exercise the raw
//                                   generated surface by design.
//   4. dynamic `import(".../bindings/...")` inside `frontend/src/stores/**`
//                                 — the established lazy-load idiom that
//                                   defers generated-module loading and
//                                   breaks store↔api import cycles.
//   5. The per-file SANCTIONED_STATIC registry below — legacy static
//      consumers whose convergence is deferred (each with a reason).
//
// When one of the registered files is eventually converged into `api/`,
// remove its registry entry in the same change.

import { readdir, readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

// Registry of sanctioned static bindings imports outside src/api (rule 5).
// Keys are repo-relative POSIX paths; values state why convergence is
// deferred. Keep entries sorted.
const SANCTIONED_STATIC = new Map(
  Object.entries({
    // Monaco/LSP editor adapters: thin protocol adapters over the generated
    // lspservice module with their own monaco-facing error semantics; an
    // api/ wrapper would add nothing but indirection.
    "frontend/src/lib/codeLens.ts": "Monaco LSP code-lens adapter (thin lspservice consumer)",
    "frontend/src/lib/inlayHints.ts": "Monaco LSP inlay-hint adapter (thin lspservice consumer)",
    "frontend/src/lib/lspCompletion.ts": "Monaco LSP completion adapter (thin lspservice consumer)",
    "frontend/src/lib/semanticTokens.ts": "Monaco LSP semantic-token adapter (thin lspservice consumer)",
    // Git adapters with local errorMessage() normalization.
    "frontend/src/lib/gitRebase.ts": "rebase adapter (gitrebaseservice) with local error normalization",
    "frontend/src/lib/gitWorktree.ts": "worktree adapter (gitworktreeservice) with local error normalization",
    // Extension host sandbox bridge.
    "frontend/src/lib/extensionHost/extensionHost.ts": "extension-host task bridge (taskservice) inside the isolated host module",
    // Stores predating the api/ layer; own their error handling.
    "frontend/src/stores/debug.ts": "DAP store consumes debugservice directly (pre-api legacy)",
    "frontend/src/stores/httpClient.ts": "HTTP client store owns request/history error handling",
    "frontend/src/stores/lsp.ts": "LSP bootstrap store consumes lspservice directly (pre-api legacy)",
    "frontend/src/stores/refactor.ts": "refactor store consumes lspservice directly (pre-api legacy)",
    // Only the generated time/Duration model (no service call surface).
    "frontend/src/stores/snapshot.ts": "imports the generated time/Duration model only; service calls go through lazy import",
    // Startup wiring that runs before any api wrapper is meaningful.
    "frontend/src/main.ts": "app bootstrap wires WindowService at startup",
    // Components with a single legacy direct call each.
    "frontend/src/components/editor/CodeEditor.vue": "editor gutter reads gitservice directly (legacy)",
    "frontend/src/components/layout/DebugPanel.vue": "debug panel reads debugservice directly (legacy)",
  }),
);

const STATIC_IMPORT = /\bfrom\s+["'][^"']*\/bindings\//;
const DYNAMIC_IMPORT = /\bimport\(\s*["'][^"']*\/bindings\//;

// findDirectBindingsImports returns every direct bindings import in a source
// text: { line (1-based), kind: "static" | "dynamic", excerpt }.
export function findDirectBindingsImports(text) {
  const findings = [];
  const lines = text.split(/\r?\n/);
  for (let i = 0; i < lines.length; i += 1) {
    const line = lines[i];
    if (DYNAMIC_IMPORT.test(line)) {
      findings.push({ line: i + 1, kind: "dynamic", excerpt: line.trim() });
    } else if (STATIC_IMPORT.test(line)) {
      findings.push({ line: i + 1, kind: "static", excerpt: line.trim() });
    }
  }
  return findings;
}

// isSanctioned reports whether a direct import of `kind` in `repoPath` (POSIX,
// repo-relative) is allowed, and via which rule.
export function isSanctioned(repoPath, kind) {
  if (repoPath.startsWith("frontend/src/api/")) return { ok: true, via: "api wrapper layer" };
  if (repoPath.endsWith(".test.ts")) return { ok: true, via: "test files pin the generated surface" };
  if (repoPath.startsWith("frontend/src/e2e/")) return { ok: true, via: "e2e runtime probes use the raw surface" };
  if (kind === "dynamic" && repoPath.startsWith("frontend/src/stores/")) {
    return { ok: true, via: "stores lazy-load idiom (defers module load, breaks import cycles)" };
  }
  const reason = SANCTIONED_STATIC.get(repoPath);
  if (kind === "static" && reason) return { ok: true, via: `registered exception: ${reason}` };
  return { ok: false };
}

async function* walkSources(dir) {
  for (const entry of await readdir(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      yield* walkSources(full);
    } else if (/\.(ts|vue)$/.test(entry.name)) {
      yield full;
    }
  }
}

function toPosix(filePath) {
  return path.relative(root, filePath).split(path.sep).join("/");
}

async function main() {
  const violations = [];
  const counts = {};
  for await (const filePath of walkSources(path.join(root, "frontend", "src"))) {
    const repoPath = toPosix(filePath);
    const text = await readFile(filePath, "utf8");
    for (const finding of findDirectBindingsImports(text)) {
      const verdict = isSanctioned(repoPath, finding.kind);
      const bucket = verdict.ok
        ? verdict.via.startsWith("registered exception:")
          ? "registered exceptions (SANCTIONED_STATIC)"
          : verdict.via
        : "violations";
      counts[bucket] = (counts[bucket] || 0) + 1;
      if (!verdict.ok) {
        violations.push(`${repoPath}:${finding.line} [${finding.kind}] ${finding.excerpt}`);
      }
    }
  }
  if (violations.length) {
    console.error(`[bindings-imports] ${violations.length} unregistered direct bindings import(s):`);
    for (const violation of violations) console.error(`- ${violation}`);
    console.error("Route new code through frontend/src/api/* wrappers, or register the file");
    console.error("with a reason in SANCTIONED_STATIC in scripts/check-bindings-imports.mjs.");
    process.exit(1);
  }
  const summary = Object.entries(counts)
    .sort()
    .map(([bucket, count]) => `${count} ${bucket}`)
    .join(", ");
  console.log(`[bindings-imports] OK - direct bindings imports all sanctioned (${summary || "none found"})`);
}

const isCli = process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href;
if (isCli) {
  await main();
}
