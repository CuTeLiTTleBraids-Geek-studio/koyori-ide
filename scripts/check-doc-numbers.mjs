/**
 * prompt-5 Task F/I — prevent README / code numeric drift (e.g. MAX_TOOL_CALLS).
 */
import fs from "fs";
import path from "path";
import { fileURLToPath } from "url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const agentTs = fs.readFileSync(path.join(root, "frontend", "src", "stores", "agent.ts"), "utf8");
const m = agentTs.match(/export const MAX_TOOL_CALLS\s*=\s*(\d+)/) ||
  agentTs.match(/const MAX_TOOL_CALLS\s*=\s*(\d+)/);
if (!m) {
  console.error("[docs] MAX_TOOL_CALLS not found in agent.ts");
  process.exit(1);
}
const max = m[1];
const readme = fs.readFileSync(path.join(root, "README.md"), "utf8");
if (!readme.includes(`${max} 次工具`) && !readme.includes(`${max} tool`)) {
  console.error(`[docs] README does not mention tool budget ${max}`);
  process.exit(1);
}
console.log(`[docs] OK — MAX_TOOL_CALLS=${max} aligned with README`);
