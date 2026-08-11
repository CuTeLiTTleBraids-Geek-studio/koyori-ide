#!/usr/bin/env node

import {
  auditBindingsDirectory,
  auditFrontendBindingUsage,
  bindingsDirectory,
  compareBindingDirectories,
  formatErrors,
  generateBindings,
  loadBindingsManifest,
  readWailsPin,
  withTemporaryBindings,
} from "./lib/wails-bindings.mjs";

try {
  const pinnedVersion = await readWailsPin();
  const manifest = await loadBindingsManifest(pinnedVersion);
  const errors = [
    ...await auditBindingsDirectory(bindingsDirectory, manifest),
    ...await auditFrontendBindingUsage(),
  ];
  await withTemporaryBindings("koyori-ide-bindings-check-", async (expected) => {
    await generateBindings(expected, { pinnedVersion });
    errors.push(...await auditBindingsDirectory(expected, manifest));
    errors.push(...await compareBindingDirectories(bindingsDirectory, expected));
  });
  if (errors.length > 0) throw new Error(formatErrors(errors).trimEnd());
  console.log(
    `[bindings] OK - pinned ${pinnedVersion}, manifest and generated tree match, ByName=0`,
  );
} catch (error) {
  console.error(error instanceof Error ? error.message : String(error));
  process.exitCode = 1;
}
