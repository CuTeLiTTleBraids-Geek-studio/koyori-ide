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

const bootstrap = fs.readFileSync(path.join(root, "bootstrap_services.go"), "utf8");
const serviceCount = (bootstrap.match(/application\.NewService\(/g) || []).length;
if (serviceCount !== 47) {
  console.error(`[docs] bootstrap_services.go registers ${serviceCount} services, expected 47`);
  process.exit(1);
}
if (!readme.includes("47 个后端服务") || !readme.includes("47 个后端服务的装配") || !readme.includes("47 服务")) {
  console.error("[docs] README service count is not 47");
  process.exit(1);
}
const arch = fs.readFileSync(path.join(root, "docs", "ARCHITECTURE.md"), "utf8");
if (!arch.includes("47 Go services")) {
  console.error("[docs] ARCHITECTURE.md service count is not 47");
  process.exit(1);
}

console.log(`[docs] OK — MAX_TOOL_CALLS=${max} aligned with README; services=${serviceCount}`);
