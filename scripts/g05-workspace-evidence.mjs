import { createHash } from "node:crypto";
import { mkdir, readFile, rename, writeFile } from "node:fs/promises";
import path from "node:path";
import process from "node:process";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const evidenceDir = path.join(root, "build", "e2e-evidence", "p9-g05");
const goLogPath = path.join(evidenceDir, "g05-service-test.log");
const frontendLogPath = path.join(evidenceDir, "g05-frontend-test.log");
const reportPath = path.join(evidenceDir, "g05-workspace-evidence.json");
const goCommand = ["go", "test", "./services", "-run", "^TestG05|^TestWorkspaceContext", "-count=1", "-v"];
const frontendCommand = process.platform === "win32" ? (process.env.ComSpec || "cmd.exe") : "npm";
const frontendTestArgs = ["test", "--", "--run", "src/views/AiWindowView.test.ts", "src/stores/workspaceStore.test.ts", "src/stores/app.test.ts"];
// npm.cmd is a batch file on Windows and cannot be spawned directly by Node's
// synchronous child-process API in this environment. Route it through the
// system command interpreter while keeping the test arguments fixed.
const frontendArgs = process.platform === "win32"
  ? ["/d", "/s", "/c", ["npm.cmd", ...frontendTestArgs].join(" ")]
  : frontendTestArgs;
const sourceFiles = [
  "main.go",
  "services/project_service.go",
  "services/project_workspace_authority.go",
  "services/project_workspace_clear.go",
  "services/workspace_context.go",
  "services/workspace_g05_test.go",
  "frontend/src/lib/crossWindowSync.ts",
  "frontend/src/stores/appActions.ts",
  "frontend/src/stores/workspaceStore.ts",
  "frontend/src/stores/workspaceStore.test.ts",
  "frontend/src/views/AiWindowView.vue",
  "frontend/src/views/AiWindowView.test.ts",
  "scripts/g05-workspace-evidence.mjs",
];

function sha256(value) {
  return createHash("sha256").update(value).digest("hex");
}

async function atomicWrite(filePath, content) {
  const temporary = `${filePath}.tmp-${process.pid}`;
  await writeFile(temporary, content, "utf8");
  await rename(temporary, filePath);
}

async function sourceFingerprint() {
  const hash = createHash("sha256");
  const files = [];
  for (const relativePath of [...sourceFiles].sort()) {
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
const goRun = spawnSync(goCommand[0], goCommand.slice(1), {
  cwd: root,
  env: process.env,
  encoding: "utf8",
  maxBuffer: 64 * 1024 * 1024,
});
const goLog = [goRun.stdout ?? "", goRun.stderr ?? ""].filter(Boolean).join("\n");
await atomicWrite(goLogPath, goLog);

const frontendRun = spawnSync(frontendCommand, frontendArgs, {
  cwd: path.join(root, "frontend"),
  env: process.env,
  encoding: "utf8",
  maxBuffer: 64 * 1024 * 1024,
});
const frontendLog = [frontendRun.stdout ?? "", frontendRun.stderr ?? ""].filter(Boolean).join("\n");
await atomicWrite(frontendLogPath, frontendLog);

const requiredGoTests = [
  "TestG05WorkspaceSnapshotPublishesRootRootsAndGeneration",
  "TestG05ConcurrentWorkspaceSwitchesSerializeAndLatestSnapshotWins",
  "TestG05WorkspaceSwitchFailureRollsBackAndDoesNotBroadcast",
  "TestG05WindowReopenReadsSameSnapshotAndClearRollsBackOnFailure",
  "TestWorkspaceContextSettersAreHiddenFromRenderer",
];
const missingGoTests = requiredGoTests.filter((name) => !goLog.includes(`--- PASS: ${name}`));
const frontendSummary = frontendLog.match(/Test Files\s+(\d+) passed[\s\S]*?Tests\s+(\d+) passed/);
const fingerprint = await sourceFingerprint();
const goExitCode = goRun.status ?? 1;
const frontendExitCode = frontendRun.status ?? 1;
const passed = goExitCode === 0 && frontendExitCode === 0 && missingGoTests.length === 0 && frontendSummary !== null;
const report = {
  schemaVersion: 1,
  goal: "P9-G05",
  status: passed ? "service-and-renderer-verified" : "failed",
  evidenceLevel: ["T", "I"],
  packagedVerified: false,
  platform: `${process.platform}/${process.arch}`,
  startedAt,
  finishedAt: new Date().toISOString(),
  commands: {
    go: goCommand.join(" "),
    frontend: process.platform === "win32"
      ? ["npm.cmd", ...frontendTestArgs].join(" ")
      : [frontendCommand, ...frontendArgs].join(" "),
  },
  results: {
    goExitCode,
    frontendExitCode,
    requiredGoTests,
    missingGoTests,
    frontendTestFiles: frontendSummary ? Number(frontendSummary[1]) : null,
    frontendTests: frontendSummary ? Number(frontendSummary[2]) : null,
  },
  logs: {
    go: { path: path.relative(root, goLogPath).replaceAll("\\", "/"), sha256: sha256(goLog), bytes: Buffer.byteLength(goLog) },
    frontend: { path: path.relative(root, frontendLogPath).replaceAll("\\", "/"), sha256: sha256(frontendLog), bytes: Buffer.byteLength(frontendLog) },
  },
  sourceFingerprintSha256: fingerprint.sha256,
  sourceFiles: fingerprint.files,
  limitations: [
    "This report covers real ProjectService/WorkspaceContext integration and renderer tests; it is not packaged desktop evidence.",
    "P9-G05 packaged multi-window Search/AI/Terminal evidence remains U until a pinned Wails packaged multi-window run records real logs and screenshots.",
    "The repository .git directory is empty, so no commit or tracked-file ownership is asserted.",
  ],
};
await atomicWrite(reportPath, `${JSON.stringify(report, null, 2)}\n`);
process.stdout.write(goLog);
process.stdout.write(`\n[g05-evidence] frontend log=${path.relative(root, frontendLogPath)}\n`);
process.stdout.write(`[g05-evidence] report=${path.relative(root, reportPath)}\n`);
process.stdout.write(`[g05-evidence] report-sha256=${sha256(JSON.stringify(report))}\n`);
if (!passed) process.exitCode = 1;
