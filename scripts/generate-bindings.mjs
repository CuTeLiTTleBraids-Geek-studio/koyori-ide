#!/usr/bin/env node

import {
  auditBindingsDirectory,
  auditFrontendBindingUsage,
  bindingsDirectory,
  formatErrors,
  generateBindings,
  loadBindingsManifest,
  readWailsPin,
} from "./lib/wails-bindings.mjs";

try {
  const pinnedVersion = await readWailsPin();
  const extraArgs = [];
  for (let index = 2; index < process.argv.length; index += 1) {
    const argument = process.argv[index];
    if (argument === "--build-flags") {
      const value = process.argv[index + 1];
      if (!value) throw new Error("[bindings] --build-flags requires a value");
      extraArgs.push("-f", value);
      index += 1;
    } else if (argument === "--obfuscated") {
      extraArgs.push("-obfuscated");
    } else {
      throw new Error(`[bindings] unsupported generator option: ${argument}`);
    }
  }
  const result = await generateBindings(bindingsDirectory, {
    pinnedVersion,
    extraArgs,
  });
  const manifest = await loadBindingsManifest(pinnedVersion);
  const errors = [
    ...await auditBindingsDirectory(bindingsDirectory, manifest),
    ...await auditFrontendBindingUsage(),
  ];
  if (errors.length > 0) throw new Error(formatErrors(errors).trimEnd());
  console.log(
    `[bindings] generated and verified with ${pinnedVersion} (${result.runner.label})`,
  );
} catch (error) {
  console.error(error instanceof Error ? error.message : String(error));
  process.exitCode = 1;
}
