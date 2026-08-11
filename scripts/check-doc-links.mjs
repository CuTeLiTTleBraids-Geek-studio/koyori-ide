import fs from "node:fs";
import path from "node:path";

const root = path.resolve(import.meta.dirname, "..");
const documents = [
  path.join(root, "README.md"),
  ...fs.readdirSync(path.join(root, "docs"), { recursive: true })
    .filter((entry) => entry.endsWith(".md"))
    .map((entry) => path.join(root, "docs", entry)),
  ...fs.readdirSync(path.join(root, ".github"))
    .filter((entry) => entry.endsWith(".md"))
    .map((entry) => path.join(root, ".github", entry)),
];

const failures = [];
const linkPattern = /!?(?:\[[^\]]*\])\(([^)]+)\)/g;

for (const document of documents) {
  const content = fs.readFileSync(document, "utf8");
  for (const match of content.matchAll(linkPattern)) {
    const rawTarget = match[1].trim().replace(/^<|>$/g, "");
    if (/^(?:https?:|mailto:|#)/i.test(rawTarget)) continue;

    const target = decodeURIComponent(rawTarget.split("#", 1)[0]);
    if (!target) continue;
    const resolved = path.resolve(path.dirname(document), target);
    if (!fs.existsSync(resolved)) {
      failures.push(`${path.relative(root, document)} -> ${rawTarget}`);
    }
  }
}

if (failures.length > 0) {
  console.error(`[doc-links] ${failures.length} missing local target(s):`);
  for (const failure of failures) console.error(`- ${failure}`);
  process.exit(1);
}

console.log(`[doc-links] OK - checked ${documents.length} Markdown files`);
