import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdtemp, readFile, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import test from "node:test";

const execFileAsync = promisify(execFile);
const checker = path.join(path.dirname(fileURLToPath(import.meta.url)), "check-sbom-artifact.mjs");

async function fixture() {
  const directory = await mkdtemp(path.join(os.tmpdir(), "koyori-ide-sbom-"));
  const artifact = path.join(directory, "koyori-ide-v0.2.0-windows-amd64.zip");
  const sbom = path.join(directory, `${path.basename(artifact)}.sbom.spdx.json`);
  const digest = createHash("sha256").update("artifact").digest("hex");
  await writeFile(artifact, "artifact");
  await writeFile(sbom, JSON.stringify({
    spdxVersion: "SPDX-2.3",
    packages: [{
      name: path.basename(artifact),
      primaryPackagePurpose: "FILE",
      checksums: [{ algorithm: "SHA256", checksumValue: digest }],
    }],
  }));
  return { artifact, sbom, digest };
}

test("accepts a uniquely bound SPDX artifact root", async () => {
  const { artifact, sbom, digest } = await fixture();
  const result = await execFileAsync(process.execPath, [checker, artifact, sbom]);
  assert.match(result.stdout, new RegExp(digest));
});

test("rejects an SPDX root with the wrong artifact digest", async () => {
  const { artifact, sbom } = await fixture();
  const document = JSON.parse(await readFile(sbom, "utf8"));
  document.packages[0].checksums[0].checksumValue = "0".repeat(64);
  await writeFile(sbom, JSON.stringify(document));
  await assert.rejects(
    execFileAsync(process.execPath, [checker, artifact, sbom]),
    (error) => error.code === 1 && error.stderr.includes("not uniquely bound"),
  );
});

test("rejects an SPDX document without a unique file root", async () => {
  const { artifact, sbom, digest } = await fixture();
  const document = JSON.parse(await readFile(sbom, "utf8"));
  document.packages = [{ name: "other", checksums: [{ algorithm: "SHA256", checksumValue: digest }] }];
  await writeFile(sbom, JSON.stringify(document));
  await assert.rejects(
    execFileAsync(process.execPath, [checker, artifact, sbom]),
    (error) => error.code === 1 && error.stderr.includes("roots=0"),
  );
});
