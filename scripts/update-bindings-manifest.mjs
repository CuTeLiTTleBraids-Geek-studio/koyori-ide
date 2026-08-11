#!/usr/bin/env node

import { writeFile } from "node:fs/promises";

import {
  auditBindingsDirectory,
  createBindingsManifest,
  formatErrors,
  generateBindings,
  manifestPath,
  readWailsPin,
  validateManifestMetadata,
  withTemporaryBindings,
} from "./lib/wails-bindings.mjs";

if (!process.argv.includes("--accept-export-surface")) {
  console.error(
    "[bindings] refusing to update the export whitelist without --accept-export-surface",
  );
  process.exit(1);
}

try {
  const pinnedVersion = await readWailsPin();
  await withTemporaryBindings("koyori-ide-bindings-manifest-", async (generated) => {
    await generateBindings(generated, { pinnedVersion });
    const manifest = await createBindingsManifest(generated, pinnedVersion);
    const errors = [
      ...validateManifestMetadata(manifest, pinnedVersion),
      ...await auditBindingsDirectory(generated, manifest),
    ];
    if (errors.length > 0) throw new Error(formatErrors(errors).trimEnd());
    await writeFile(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`, "utf8");
    console.log(
      `[bindings] accepted ${Object.keys(manifest.exports).length} service modules and ${Object.keys(manifest.files).length} generated files`,
    );
  });
} catch (error) {
  console.error(error instanceof Error ? error.message : String(error));
  process.exitCode = 1;
}
