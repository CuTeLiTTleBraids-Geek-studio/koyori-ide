import fs from "fs";
const t = fs.readFileSync("vue-tsc-errors.txt", "utf8");
const lines = t.split(/\n/).filter((l) => l.includes("error TS"));
const files = {};
for (const l of lines) {
  const m = l.match(/^([^(]+)/);
  if (m) files[m[1]] = (files[m[1]] || 0) + 1;
}
console.log("TOTAL", lines.length);
Object.entries(files)
  .sort((a, b) => b[1] - a[1])
  .forEach(([f, c]) => console.log(c, f));
