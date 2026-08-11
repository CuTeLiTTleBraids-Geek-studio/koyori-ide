#!/usr/bin/env node

import { createHash } from "node:crypto";
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
        digest: { gitCommit: process.env.GITHUB_SHA },
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
