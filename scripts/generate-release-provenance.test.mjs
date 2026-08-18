import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const script = path.join(path.dirname(fileURLToPath(import.meta.url)), "generate-release-provenance.mjs");
const checkoutCommit = execFileSync("git", ["rev-parse", "--verify", "HEAD"], { encoding: "utf8" }).trim();
const env = {
  ...process.env,
  GITHUB_SERVER_URL: "https://github.example",
  GITHUB_REPOSITORY: "example/koyori-ide",
  GITHUB_REF: "refs/tags/v0.2.0",
  GITHUB_SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  RELEASE_COMMIT_SHA: checkoutCommit,
  GITHUB_RUN_ID: "1234",
  GITHUB_RUN_ATTEMPT: "2",
  GITHUB_WORKFLOW_REF: "example/koyori-ide/.github/workflows/release.yml@refs/tags/v0.2.0",
};

test("writes an unsigned in-toto statement with sorted SHA-256 subjects", () => {
  const dir = mkdtempSync(path.join(tmpdir(), "koyori-ide-provenance-"));
  const first = path.join(dir, "z-artifact.zip");
  const second = path.join(dir, "a-sbom.spdx.json");
  const output = path.join(dir, "provenance.intoto.jsonl");
  writeFileSync(first, "artifact");
  writeFileSync(second, "sbom");

  execFileSync(process.execPath, [script, output, first, second], { env });
  const raw = readFileSync(output, "utf8");
  assert.equal(raw.trim().split("\n").length, 1);
  const statement = JSON.parse(raw);
  assert.equal(statement._type, "https://in-toto.io/Statement/v1");
  assert.equal(statement.predicateType, "https://slsa.dev/provenance/v1");
  assert.deepEqual(statement.subject.map(({ name }) => name), ["a-sbom.spdx.json", "z-artifact.zip"]);
  assert.match(statement.subject[0].digest.sha256, /^[a-f0-9]{64}$/);
  assert.equal(statement.predicate.buildDefinition.internalParameters.signatureStatus, "unsigned");
  assert.match(statement.predicate.buildDefinition.internalParameters.statementKind, /not a signed attestation/);
  assert.notEqual(env.GITHUB_SHA, env.RELEASE_COMMIT_SHA);
  assert.equal(statement.predicate.buildDefinition.resolvedDependencies[0].digest.gitCommit, checkoutCommit);
});

test("fails closed when GitHub identity is incomplete", () => {
  const dir = mkdtempSync(path.join(tmpdir(), "koyori-ide-provenance-"));
  const subject = path.join(dir, "artifact.zip");
  writeFileSync(subject, "artifact");
  assert.throws(() => execFileSync(process.execPath, [script, path.join(dir, "out.jsonl"), subject], {
    env: { ...env, GITHUB_SHA: "" },
    stdio: "pipe",
  }));
});

test("fails closed when the declared release commit is not the checkout", () => {
  const dir = mkdtempSync(path.join(tmpdir(), "koyori-ide-provenance-"));
  const subject = path.join(dir, "artifact.zip");
  writeFileSync(subject, "artifact");
  assert.throws(() => execFileSync(process.execPath, [script, path.join(dir, "out.jsonl"), subject], {
    env: { ...env, RELEASE_COMMIT_SHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" },
    stdio: "pipe",
  }));
});
