#!/usr/bin/env node

import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { readFile, writeFile } from "node:fs/promises";
import path from "node:path";

const [outputPath, ...subjectPaths] = process.argv.slice(2);
if (!outputPath || subjectPaths.length === 0) {
  console.error("usage: generate-release-provenance.mjs <output> <subject> [subject ...]");
  process.exit(2);
}

const requiredEnvironment = [
  "GITHUB_SERVER_URL",
  "GITHUB_REPOSITORY",
  "GITHUB_REF",
  "GITHUB_SHA",
  "RELEASE_COMMIT_SHA",
  "GITHUB_RUN_ID",
  "GITHUB_RUN_ATTEMPT",
  "GITHUB_WORKFLOW_REF",
];
for (const name of requiredEnvironment) {
  if (!process.env[name]) {
    console.error(`[provenance] required environment variable is missing: ${name}`);
    process.exit(1);
  }
}

const shaPattern = /^[0-9a-f]{40}$/;
for (const name of ["GITHUB_SHA", "RELEASE_COMMIT_SHA"]) {
  if (!shaPattern.test(process.env[name])) {
    console.error(`[provenance] ${name} must be a lowercase 40-character Git SHA`);
    process.exit(1);
  }
}

let checkoutCommit;
try {
  checkoutCommit = execFileSync("git", ["rev-parse", "--verify", "HEAD"], {
    encoding: "utf8",
    windowsHide: true,
  }).trim();
} catch {
  console.error("[provenance] unable to resolve the checked-out commit");
  process.exit(1);
}
if (!shaPattern.test(checkoutCommit) || checkoutCommit !== process.env.RELEASE_COMMIT_SHA) {
  console.error(`[provenance] checked-out commit ${checkoutCommit || "<invalid>"} does not match RELEASE_COMMIT_SHA`);
  process.exit(1);
}

const subjects = [];
const names = new Set();
for (const subjectPath of subjectPaths) {
  const name = path.basename(subjectPath);
  if (names.has(name)) {
    console.error(`[provenance] duplicate subject basename: ${name}`);
    process.exit(1);
  }
  names.add(name);
  const content = await readFile(subjectPath);
  subjects.push({
    name,
    digest: { sha256: createHash("sha256").update(content).digest("hex") },
  });
}
subjects.sort((left, right) => left.name.localeCompare(right.name, "en"));

const server = process.env.GITHUB_SERVER_URL.replace(/\/$/, "");
const repository = process.env.GITHUB_REPOSITORY;
const runURL = `${server}/${repository}/actions/runs/${process.env.GITHUB_RUN_ID}/attempts/${process.env.GITHUB_RUN_ATTEMPT}`;
const statement = {
  _type: "https://in-toto.io/Statement/v1",
  subject: subjects,
  predicateType: "https://slsa.dev/provenance/v1",
  predicate: {
    buildDefinition: {
      buildType: `${server}/${repository}/.github/workflows/release.yml`,
      externalParameters: {
        ref: process.env.GITHUB_REF,
        workflowRef: process.env.GITHUB_WORKFLOW_REF,
      },
      internalParameters: {
        signatureStatus: "unsigned",
        statementKind: "release metadata; not a signed attestation",
      },
      resolvedDependencies: [{
        uri: `git+${server}/${repository}@${process.env.GITHUB_REF}`,
        digest: { gitCommit: process.env.RELEASE_COMMIT_SHA },
      }],
    },
    runDetails: {
      builder: { id: runURL },
      metadata: { invocationId: runURL },
    },
  },
};

await writeFile(outputPath, `${JSON.stringify(statement)}\n`, "utf8");
console.log(`[provenance] wrote unsigned statement to ${outputPath}`);
