#!/usr/bin/env node

import { checkBindingsOwnership } from "./lib/wails-bindings.mjs";

try {
  checkBindingsOwnership();
  console.log("[bindings] OK - frontend/bindings is not tracked by Git");
} catch (error) {
  console.error(error instanceof Error ? error.message : String(error));
  process.exitCode = 1;
}
