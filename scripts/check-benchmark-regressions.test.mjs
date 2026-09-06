import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const gatePath = fileURLToPath(new URL("./check-benchmark-regressions.mjs", import.meta.url));

function runGate(csv) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "koyori-benchmark-gate-"));
  try {
    const csvPath = path.join(directory, "benchstat.csv");
    fs.writeFileSync(csvPath, csv);
    return spawnSync(process.execPath, [gatePath, csvPath], { encoding: "utf8" });
  } finally {
    fs.rmSync(directory, { recursive: true, force: true });
  }
}

test("does not treat a later B/s improvement as a sec/op regression", () => {
  const result = runGate([
    "name,sec/op,base,base error,candidate,vs base,p-value",
    "BenchmarkLatency,sec/op,100ns,1%,110ns,+10.00%,(p=0.001 n=10)",
    "",
    "name,B/s,base,base error,candidate,vs base,p-value",
    "BenchmarkThroughput,B/s,100,1%,130,+30.00%,(p=0.001 n=10)",
    "",
  ].join("\n"));

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /passed \(1 comparable rows\)/);
});

test("fails for a significant sec/op regression above the threshold", () => {
  const result = runGate([
    "name,sec/op,base,base error,candidate,vs base,p-value",
    "BenchmarkLatency,sec/op,100ns,1%,121ns,+21.00%,(p=0.001 n=10)",
    "",
  ].join("\n"));

  assert.equal(result.status, 1, result.stdout);
  assert.match(result.stderr, /BenchmarkLatency sec\/op \+21\.00%/);
});

test("discovers comparison columns in benchstat tables with config keys", () => {
  const result = runGate([
    "pkg,name,sec/op,CI,sec/op,CI,vs base,P",
    "services,BenchmarkLatency,1e-7,1%,1.1e-7,1%,+10.00%,p=0.001 n=10",
    "",
  ].join("\n"));

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /passed \(1 comparable rows\)/);
});
