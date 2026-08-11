import { describe, expect, it } from "vitest";

import { JSON_SCHEMA_ASSOCIATIONS, jsonSchemaKindForFilePath } from "./jsonSchemaConfig";

describe("JSON schema file configuration", () => {
  it.each([
    ["/workspace/tsconfig.json", "tsconfig"],
    ["/workspace/tsconfig.web.json", "tsconfig"],
    ["/workspace/jsconfig.json", "jsconfig"],
    ["/workspace/package.json", "package"],
    ["/workspace/package-lock.json", undefined],
  ])("selects %s", (filePath, kind) => {
    expect(jsonSchemaKindForFilePath(filePath)).toBe(kind);
  });

  it("uses the three fixed HTTPS SchemaStore associations", () => {
    expect(JSON_SCHEMA_ASSOCIATIONS.map((association) => association.url)).toEqual([
      "https://json.schemastore.org/tsconfig.json",
      "https://json.schemastore.org/jsconfig.json",
      "https://json.schemastore.org/package.json",
    ]);
  });
});
