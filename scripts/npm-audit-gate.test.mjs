import assert from "node:assert/strict";
import test from "node:test";

import {
  auditArgs,
  npm,
  registry,
  runDependencyGate,
  syncArgs,
} from "./npm-audit-gate.mjs";

const success = Object.freeze({ status: 0, stdout: "", stderr: "" });

function validLockfile() {
  return {
    packages: {
      "": { name: "fixture", version: "1.0.0" },
      "node_modules/example": {
        version: "1.0.0",
        resolved: "https://registry.npmjs.org/example/-/example-1.0.0.tgz",
        integrity: "sha512-Zml4dHVyZS1pYWNrYWdl",
      },
      "node_modules/example/node_modules/bundled": {
        version: "1.0.0",
        inBundle: true,
      },
    },
  };
}

function sequenceRunner(responses = [success, success]) {
  const calls = [];
  const npmRunner = (args) => {
    calls.push([...args]);
    return responses[calls.length - 1] ?? success;
  };
  return { calls, npmRunner };
}

function runFixture(lockfile, responses) {
  const runner = sequenceRunner(responses);
  const failures = runDependencyGate(lockfile, {
    npmRunner: runner.npmRunner,
    log() {},
  });
  return { ...runner, failures };
}

test("rejects a registry package without an SRI digest", () => {
  const lockfile = validLockfile();
  delete lockfile.packages["node_modules/example"].integrity;

  const { failures } = runFixture(lockfile);

  assert.equal(failures.length, 1);
  assert.match(failures[0], /without a committed resolved URL and SRI digest/);
  assert.match(failures[0], /node_modules\/example/);
});

test("rejects a resolved URL outside the official npm registry", () => {
  const lockfile = validLockfile();
  lockfile.packages["node_modules/example"].resolved =
    "https://registry.example.invalid/example-1.0.0.tgz";

  const { failures } = runFixture(lockfile);

  assert.equal(failures.length, 1);
  assert.match(failures[0], /non-official resolved URL/);
  assert.match(failures[0], /expected https:\/\/registry\.npmjs\.org\//);
});

test("fails when npm audit reports a high or critical advisory", () => {
  const auditFailure = {
    status: 1,
    stdout: "",
    stderr: "found 1 high severity vulnerability",
  };

  const { failures } = runFixture(validLockfile(), [auditFailure, success]);

  assert.equal(failures.length, 1);
  assert.match(
    failures[0],
    /npm audit reports high\/critical advisories \(exit 1\)/,
  );
  assert.match(failures[0], /1 high severity vulnerability/);
});

test("fails closed when npm audit cannot be spawned", () => {
  const auditFailure = { error: new Error("network command unavailable") };

  const { failures } = runFixture(validLockfile(), [auditFailure, success]);

  assert.deepEqual(failures, [
    "npm audit spawn failed: network command unavailable",
  ]);
});

test("fails when npm ci rejects an out-of-sync lockfile", () => {
  const syncFailure = {
    status: 1,
    stdout: "",
    stderr: "npm error package-lock is out of date",
  };

  const { failures } = runFixture(validLockfile(), [success, syncFailure]);

  assert.equal(failures.length, 1);
  assert.match(
    failures[0],
    /npm ci --dry-run rejected package\.json\/package-lock\.json \(exit 1\)/,
  );
  assert.match(failures[0], /package-lock is out of date/);
});

test("uses fixed audit and lock-sync arguments", () => {
  const { calls, failures } = runFixture(validLockfile());

  assert.deepEqual(failures, []);
  assert.deepEqual(calls, [
    [
      "audit",
      "--package-lock-only",
      "--audit-level=high",
      `--registry=${registry}`,
    ],
    ["ci", "--dry-run", "--ignore-scripts", "--no-audit"],
  ]);
  assert.deepEqual([...auditArgs], calls[0]);
  assert.deepEqual([...syncArgs], calls[1]);
});

test("forces the official registry in the spawned npm environment", () => {
  let invocation;
  const spawn = (command, args, options) => {
    invocation = { command, args, options };
    return success;
  };

  npm(["ci", "--dry-run", "--ignore-scripts"], {
    spawn,
    platform: "linux",
    environment: { SENTINEL: "preserved" },
    workingDirectory: "/fixture/frontend",
  });

  assert.equal(invocation.command, "npm");
  assert.deepEqual(invocation.args, ["ci", "--dry-run", "--ignore-scripts"]);
  assert.equal(invocation.options.cwd, "/fixture/frontend");
  assert.equal(invocation.options.env.SENTINEL, "preserved");
  assert.equal(invocation.options.env.npm_config_registry, registry);
  assert.equal(invocation.options.timeout, 300_000);
});

test("keeps the hardened cmd.exe invocation on Windows", () => {
  let invocation;
  const spawn = (command, args, options) => {
    invocation = { command, args, options };
    return success;
  };

  npm(["audit", "--package-lock-only"], { spawn, platform: "win32" });

  assert.equal(invocation.command, "cmd.exe");
  assert.deepEqual(invocation.args, [
    "/d",
    "/s",
    "/c",
    "npm",
    "audit",
    "--package-lock-only",
  ]);
});
