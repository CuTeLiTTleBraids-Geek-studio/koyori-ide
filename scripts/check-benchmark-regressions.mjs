#!/usr/bin/env node

import fs from "node:fs";
import process from "node:process";

const [csvPath] = process.argv.slice(2);
if (!csvPath) {
  console.error("usage: check-benchmark-regressions.mjs <benchstat.csv>");
  process.exit(2);
}

const csv = fs.readFileSync(csvPath, "utf8");
// benchstat separates metric tables with blank lines. Keep those separators so
// a row from a later throughput table cannot be evaluated as sec/op.
const rows = csv.split(/\r?\n/).map((line) => line.trim() === "" ? [] : parseCsvLine(line));
const metrics = new Set(["sec/op", "B/op", "allocs/op"]);
let comparableRows = 0;
const regressions = [];

for (let index = 0; index < rows.length; index += 1) {
  const header = rows[index];
  if (header.length < 6 || header[5] !== "vs base" || !metrics.has(header[1])) {
    continue;
  }
  const metric = header[1];
  for (let rowIndex = index + 1; rowIndex < rows.length; rowIndex += 1) {
    const row = rows[rowIndex];
    if (row.length === 0 || row.every((value) => value === "")) {
      break;
    }
    // Also stop at the next table header in case an upstream formatter omits
    // the usual blank separator.
    if (row[5] === "vs base") {
      break;
    }
    if (row[0] === "geomean" || row[0]) {
      const delta = row[5] ?? "";
      if (delta === "") {
        continue;
      }
      comparableRows += 1;
      if (delta === "~") {
        continue;
      }
      const match = /^\+([0-9]+(?:\.[0-9]+)?)%$/.exec(delta);
      if (match && Number(match[1]) > 20 && row[6] !== "~") {
        regressions.push(`${row[0]} ${metric} ${delta} (${row[6]})`);
      }
    }
  }
}

if (comparableRows === 0) {
  console.error("benchstat produced no comparable sec/op, B/op, or allocs/op rows");
  process.exit(1);
}
if (regressions.length > 0) {
  console.error("significant benchmark regressions (>20%) detected:");
  for (const regression of regressions) console.error(`- ${regression}`);
  process.exit(1);
}
console.log(`benchmark regression gate passed (${comparableRows} comparable rows)`);

function parseCsvLine(line) {
  const values = [];
  let value = "";
  let quoted = false;
  for (let index = 0; index < line.length; index += 1) {
    const character = line[index];
    if (character === '"') {
      if (quoted && line[index + 1] === '"') {
        value += '"';
        index += 1;
      } else {
        quoted = !quoted;
      }
    } else if (character === "," && !quoted) {
      values.push(value);
      value = "";
    } else {
      value += character;
    }
  }
  values.push(value);
  return values;
}
