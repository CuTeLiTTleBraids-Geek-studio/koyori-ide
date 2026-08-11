#!/usr/bin/env node

import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import path from "node:path";

const [artifactPath, sbomPath] = process.argv.slice(2);
if (!artifactPath || !sbomPath) {
  console.error("usage: check-sbom-artifact.mjs <artifact> <sbom.spdx.json>");
  process.exit(2);
}

const artifact = await readFile(artifactPath).catch(() => null);
if (!artifact) {
  console.error(`[sbom] artifact is missing or unreadable: ${artifactPath}`);
  process.exit(1);
}

let sbom;
try {
  sbom = JSON.parse(await readFile(sbomPath, "utf8"));
} catch (error) {
  console.error(`[sbom] invalid JSON: ${sbomPath} (${error.message})`);
  process.exit(1);
}

if (sbom.spdxVersion !== "SPDX-2.3") {
  console.error(`[sbom] expected SPDX-2.3, found ${sbom.spdxVersion ?? "missing"}`);
  process.exit(1);
}

const expectedDigest = createHash("sha256").update(artifact).digest("hex");
const artifactName = path.basename(artifactPath);
const roots = (sbom.packages ?? []).filter((entry) =>
  entry.primaryPackagePurpose === "FILE" || entry.name === artifactName,
);
const matches = roots.filter((entry) =>
  (entry.checksums ?? []).some(
    (checksum) => checksum.algorithm === "SHA256" && checksum.checksumValue?.toLowerCase() === expectedDigest,
  ),
);

if (roots.length !== 1 || matches.length !== 1) {
  console.error(
    `[sbom] artifact digest is not uniquely bound in SPDX root (roots=${roots.length}, matches=${matches.length}, expected=${expectedDigest})`,
  );
  process.exit(1);
}

console.log(`[sbom] PASS: ${artifactName} is bound to SPDX root SHA-256 ${expectedDigest}`);
