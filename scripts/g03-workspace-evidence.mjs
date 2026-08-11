import { createHash } from "node:crypto";
import { mkdir, readFile, rename, writeFile } from "node:fs/promises";
import path from "node:path";
import process from "node:process";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(scriptDir, "..");
const evidenceDir = path.join(root, "build", "e2e-evidence", "p9-g03");
const logPath = path.join(evidenceDir, "g03-workspace-test.log");
const reportPath = path.join(evidenceDir, "g03-workspace-evidence.json");
const command = ["go", "test", "./services", "-run", "^TestG03", "-count=1", "-v"];

const requiredTests = [
  "TestG03SearchRejectsEmptyWorkspaceBeforeReading",
  "TestG03MCPConnectRejectsEmptyWorkspaceBeforeProcessStart",
  "TestG03SymbolIndexRejectsEmptyWorkspaceInsteadOfEmptySuccess",
  "TestG03LSPRejectsEmptyWorkspaceBeforeServerResolution",
  "TestG03FileRevealRejectsEmptyWorkspaceWithoutLaunching",
  "TestG03WindowOpenRejectsEmptyWorkspaceWithoutLaunching",
  "TestG03SearchRejectsResultsAfterWorkspaceSwitch",
  "TestG03SymbolIndexRejectsPublishAfterWorkspaceSwitch",
  "TestG03MCPRejectsResultsAfterWorkspaceSwitch",
  "TestG03MCPRealSubprocessBindsAndRevokesWorkspaceGeneration",
  "TestG03WorkspaceLeaseSerializesProcessStartAgainstSwitch",
  "TestG03MCPApprovalLeaseCannotSelectSameNamedClientAfterSwitch",
];

if (process.platform === "win32") {
  requiredTests.push(
    "TestG03WindowsWorkspaceCaseVariantKeepsGeneration",
    "TestG03WindowsWorkspaceJunctionUsesTargetIdentity",
    "TestG03WindowsUNCIdentityBoundaries",
  );
}

const sourceFiles = [
  "main.go",
  "services/workspace_context.go",
  "services/pathsec.go",
  "services/project_service.go",
  "services/search_service.go",
  "services/mcp_service.go",
  "services/trusted_wiring.go",
  "services/lsp_service_session.go",
  "services/lsp_service_server.go",
  "services/symbol_index_service.go",
  "services/file_service.go",
  "services/window_service.go",
  "services/g03_workspace_fail_closed_test.go",
  "services/g03_workspace_windows_test.go",
  "frontend/src/views/AiWindowView.vue",
  "frontend/src/views/AiWindowView.test.ts",
  "scripts/g03-workspace-evidence.mjs",
];

function sha256(content) {
  return createHash("sha256").update(content).digest("hex");
}

async function atomicWrite(filePath, content) {
  const temporary = `${filePath}.tmp-${process.pid}`;
  await writeFile(temporary, content);
  await rename(temporary, filePath);
}

async function sourceFingerprint() {
  const hash = createHash("sha256");
  const files = [];
  for (const relativePath of sourceFiles.toSorted()) {
    const content = await readFile(path.join(root, relativePath));
    const digest = sha256(content);
    files.push({ path: relativePath, sha256: digest, bytes: content.length });
    hash.update(relativePath);
    hash.update("\0");
    hash.update(digest);
    hash.update("\n");
  }
  return { sha256: hash.digest("hex"), files };
}

await mkdir(evidenceDir, { recursive: true });
const startedAt = new Date().toISOString();
const run = spawnSync(command[0], command.slice(1), {
  cwd: root,
  env: process.env,
  encoding: "utf8",
  maxBuffer: 64 * 1024 * 1024,
});
const combinedLog = [run.stdout ?? "", run.stderr ?? ""].filter(Boolean).join("\n");
await atomicWrite(logPath, combinedLog);

const missingTests = requiredTests.filter((name) => !combinedLog.includes(`--- PASS: ${name}`));
const pidMatch = combinedLog.match(/real MCP subprocess pid=(\d+) started and was reaped on generation switch/);
const exitCode = run.status ?? 1;
const passed = exitCode === 0 && missingTests.length === 0 && pidMatch !== null;
const fingerprint = await sourceFingerprint();
const report = {
  schemaVersion: 1,
  goal: "P9-G03",
  evidenceLevel: ["T", "I"],
  platform: `${process.platform}/${process.arch}`,
  command: command.join(" "),
  startedAt,
  finishedAt: new Date().toISOString(),
  exitCode,
  passed,
  requiredTests,
  missingTests,
  realSubprocess: pidMatch ? { pid: Number(pidMatch[1]), lifecycle: "started-and-reaped-on-workspace-switch" } : null,
  log: {
    path: path.relative(root, logPath).replaceAll("\\", "/"),
    sha256: sha256(Buffer.from(combinedLog)),
    bytes: Buffer.byteLength(combinedLog),
  },
  sourceFingerprintSha256: fingerprint.sha256,
  sourceFiles: fingerprint.files,
  limitations: [
    "Evidence was produced on Windows only; it is not macOS or Linux packaged evidence.",
    "The repository .git metadata is unavailable, so no commit identity is asserted.",
    "This is a real Go child-process integration test, not packaged desktop E2E evidence.",
  ],
};
await atomicWrite(reportPath, `${JSON.stringify(report, null, 2)}\n`);

process.stdout.write(combinedLog);
process.stdout.write(`\n[g03-evidence] report=${path.relative(root, reportPath)}\n`);
process.stdout.write(`[g03-evidence] report-sha256=${sha256(await readFile(reportPath))}\n`);
process.stdout.write(`[g03-evidence] source-fingerprint=${fingerprint.sha256}\n`);
if (!passed) {
  process.exitCode = 1;
}
