// Koyori IDE 模块 · Json Schema Config。
// 喵，这是 Koyori IDE 的 Json Schema Config 模块（前端实现）~
export type JSONSchemaKind = "tsconfig" | "jsconfig" | "package";

export interface JSONSchemaAssociation {
  kind: JSONSchemaKind;
  url: string;
  fileMatch: readonly string[];
}

export const JSON_SCHEMA_ASSOCIATIONS: readonly JSONSchemaAssociation[] = [
  {
    kind: "tsconfig",
    url: "https://json.schemastore.org/tsconfig.json",
    fileMatch: ["**/tsconfig.json", "**/tsconfig.*.json"],
  },
  {
    kind: "jsconfig",
    url: "https://json.schemastore.org/jsconfig.json",
    fileMatch: ["**/jsconfig.json", "**/jsconfig.*.json"],
  },
  {
    kind: "package",
    url: "https://json.schemastore.org/package.json",
    fileMatch: ["**/package.json"],
  },
];

export function jsonSchemaKindForFilePath(filePath: string): JSONSchemaKind | undefined {
  const base = filePath.replaceAll("\\", "/").split("/").pop()?.toLowerCase() ?? "";
  if (base === "tsconfig.json" || (base.startsWith("tsconfig.") && base.endsWith(".json"))) {
    return "tsconfig";
  }
  if (base === "jsconfig.json" || (base.startsWith("jsconfig.") && base.endsWith(".json"))) {
    return "jsconfig";
  }
  if (base === "package.json") return "package";
  return undefined;
}
