/** Resolve generated Wails `T | null` collections to their natural empty value. */
// Koyori IDE 模块 · Boundary。
// 喵，这是 Koyori IDE 的 Boundary 模块（前端实现）~
export async function unwrapNullable<T>(
  promise: PromiseLike<T | null | undefined>,
  fallback: T,
): Promise<T> {
  const value = await promise;
  return value ?? fallback;
}

/** Reject an unexpected null instead of fabricating a business object. */
export async function requireNonNull<T>(
  promise: PromiseLike<T | null | undefined>,
  operation: string,
): Promise<T> {
  const value = await promise;
  if (value === null || value === undefined) {
    throw new Error(`${operation} returned no result`);
  }
  return value;
}

export function encodeWailsBytes(data: Uint8Array): string {
  let binary = "";
  for (const byte of data) binary += String.fromCharCode(byte);
  return btoa(binary);
}

export function decodeWailsBytes(raw: string): Uint8Array {
  if (!raw) return new Uint8Array(0);
  try {
    const binary = atob(raw);
    return new Uint8Array(Array.from(binary, (char) => char.charCodeAt(0)));
  } catch {
    return new Uint8Array(Array.from(raw, (char) => char.charCodeAt(0)));
  }
}

export function safeRecordFromEntries<T>(entries: Array<[string, T]>): Record<string, T> {
  return Object.fromEntries(entries);
}

export function normalizeOptionalStringMap(
  values: { [_ in string]?: string } | null | undefined,
): Record<string, string> | undefined {
  if (!values) return undefined;
  const entries: Array<[string, string]> = [];
  for (const [key, value] of Object.entries(values)) {
    if (typeof value === "string") entries.push([key, value]);
  }
  return safeRecordFromEntries(entries);
}

export type UnknownRecord = Record<string, unknown>;

export function isRecord(value: unknown): value is UnknownRecord {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function warnInvalidBoundaryValue(
  path: string,
  expected: string,
  fallbackDescription: string,
): void {
  console.warn(
    `[services] ${path} must be ${expected}; using ${fallbackDescription}.`,
  );
}

export function requiredString(
  value: unknown,
  path: string,
  fallback = "",
): string {
  if (typeof value === "string") return value;
  warnInvalidBoundaryValue(path, "a string", JSON.stringify(fallback));
  return fallback;
}

export function optionalString(value: unknown, path: string): string | undefined {
  if (value === undefined || value === null) return undefined;
  if (typeof value === "string") return value;
  warnInvalidBoundaryValue(path, "a string", "undefined");
  return undefined;
}

export function requiredFiniteNumber(
  value: unknown,
  path: string,
  fallback: number,
): number {
  if (typeof value === "number" && Number.isFinite(value)) return value;
  warnInvalidBoundaryValue(path, "a finite number", String(fallback));
  return fallback;
}

export function optionalFiniteNumber(value: unknown, path: string): number | undefined {
  if (value === undefined || value === null) return undefined;
  if (typeof value === "number" && Number.isFinite(value)) return value;
  warnInvalidBoundaryValue(path, "a finite number", "undefined");
  return undefined;
}

export function requiredInteger(
  value: unknown,
  path: string,
  fallback: number,
): number {
  if (typeof value === "number" && Number.isInteger(value)) return value;
  warnInvalidBoundaryValue(path, "an integer", String(fallback));
  return fallback;
}

export function optionalInteger(value: unknown, path: string): number | undefined {
  if (value === undefined || value === null) return undefined;
  if (typeof value === "number" && Number.isInteger(value)) return value;
  warnInvalidBoundaryValue(path, "an integer", "undefined");
  return undefined;
}

export function requiredBoolean(
  value: unknown,
  path: string,
  fallback: boolean,
): boolean {
  if (typeof value === "boolean") return value;
  warnInvalidBoundaryValue(path, "a boolean", String(fallback));
  return fallback;
}

export function optionalBoolean(value: unknown, path: string): boolean | undefined {
  if (value === undefined || value === null) return undefined;
  if (typeof value === "boolean") return value;
  warnInvalidBoundaryValue(path, "a boolean", "undefined");
  return undefined;
}

export function optionalStringArray(
  value: unknown,
  path: string,
): string[] | undefined {
  if (value === undefined || value === null) return undefined;
  if (!Array.isArray(value)) {
    warnInvalidBoundaryValue(path, "an array of strings", "undefined");
    return undefined;
  }
  const normalized = value.filter((entry): entry is string => typeof entry === "string");
  if (normalized.length !== value.length) {
    warnInvalidBoundaryValue(path, "an array of strings", "valid entries only");
  }
  return normalized;
}

export function optionalNumberArray(
  value: unknown,
  path: string,
): number[] | undefined {
  if (value === undefined || value === null) return undefined;
  if (!Array.isArray(value)) {
    warnInvalidBoundaryValue(path, "an array of numbers", "undefined");
    return undefined;
  }
  const normalized = value.filter(
    (entry): entry is number => typeof entry === "number" && Number.isFinite(entry),
  );
  if (normalized.length !== value.length) {
    warnInvalidBoundaryValue(path, "an array of numbers", "valid entries only");
  }
  return normalized;
}

export function optionalUnknownRecord(
  value: unknown,
  path: string,
): Record<string, unknown> | undefined {
  if (value === undefined || value === null) return undefined;
  if (isRecord(value)) return safeRecordFromEntries(Object.entries(value));
  warnInvalidBoundaryValue(path, "an object", "undefined");
  return undefined;
}

export function optionalStringRecord(
  value: unknown,
  path: string,
): Record<string, string> | undefined {
  if (value === undefined || value === null) return undefined;
  if (!isRecord(value)) {
    warnInvalidBoundaryValue(path, "an object with string values", "undefined");
    return undefined;
  }
  const entries: Array<[string, string]> = [];
  let invalid = false;
  for (const [key, entry] of Object.entries(value)) {
    if (typeof entry === "string") entries.push([key, entry]);
    else invalid = true;
  }
  if (invalid) {
    warnInvalidBoundaryValue(path, "an object with string values", "valid entries only");
  }
  return safeRecordFromEntries(entries);
}
