#!/usr/bin/env node

/**
 * Minimal safe zip reader used by g24-corpus-report.mjs.
 *
 * Unlike `unzip`/`yauzl` conveniences, this reader:
 *   - never writes entries to disk (memory-only, bounded by caller)
 *   - refuses entries whose normalized path escapes the package root
 *     ("..", absolute paths, drive letters)
 *   - enforces a per-entry size limit so a malicious archive cannot OOM
 *     the generator
 */

import assert from "node:assert/strict";
import { open } from "node:fs/promises";

const MAX_ENTRY_BYTES = 8 * 1024 * 1024;
const MAX_ENTRIES = 10_000;

function normalizeEntryName(name) {
  if (typeof name !== "string") return null;
  // Reject absolute paths and drive letters up front.
  if (name.startsWith("/") || /^[A-Za-z]:/.test(name) || name.includes("\\")) {
    return null;
  }
  const parts = name.split("/");
  const normalized = [];
  for (const part of parts) {
    if (part === "" || part === ".") continue;
    if (part === "..") return null;
    normalized.push(part);
  }
  if (normalized.length === 0) return null;
  return normalized.join("/");
}

export class ZipArchive {
  static async open(filePath) {
    const handle = await open(filePath, "r");
    const stat = await handle.stat();
    if (!stat.isFile()) throw new Error("zip source is not a file");
    // End-of-central-directory record: minimum 22 bytes, located at the end.
    const tail = Math.min(stat.size, 22 + 65_535);
    const buffer = Buffer.alloc(tail);
    await handle.read(buffer, 0, tail, stat.size - tail);
    let eocdOffset = -1;
    for (let index = tail - 22; index >= 0; index -= 1) {
      if (buffer.readUInt32LE(index) === 0x06054b50) {
        eocdOffset = stat.size - tail + index;
        break;
      }
    }
    if (eocdOffset < 0) throw new Error("end-of-central-directory not found");
    // EOCD record layout: sig(4) disk(2) cdDisk(2) entriesThisDisk(2)
    // entriesTotal(2) cdSize(4) cdOffset(4) commentLen(2). The tail buffer
    // was read from (size - tail); the buffer-local EOCD index is
    // (eocdOffset - (size - tail)), and entry count sits at EOCD + 10.
    const eocdIndex = eocdOffset - (stat.size - tail);
    const entryCount = buffer.readUInt16LE(eocdIndex + 10);
    if (entryCount > MAX_ENTRIES) throw new Error(`archive declares too many entries: ${entryCount}`);
    return new ZipArchive(handle, stat.size, eocdOffset);
  }

  constructor(handle, size, eocdOffset) {
    this.handle = handle;
    this.size = size;
    this.eocdOffset = eocdOffset;
    this.entries = new Map();
    this.closed = false;
  }

  /** Lazily read all local headers and build the entry index. */
  async buildIndex() {
    if (this.indexed) return;
    this.indexed = true;
    // Read the central directory records following the EOCD.
    const centralSize = 0;
    void centralSize;
    // Central directory offset is a uint32 at EOCD + 16.
    const cdOffset = await this.readUInt32At(this.eocdOffset + 16);
    const cdSize = await this.readUInt32At(this.eocdOffset + 12);
    const cdBuffer = Buffer.alloc(cdSize);
    await this.handle.read(cdBuffer, 0, cdSize, cdOffset);
    let position = 0;
    while (position + 46 <= cdBuffer.length) {
      if (cdBuffer.readUInt32LE(position) !== 0x02014b50) break;
      const method = cdBuffer.readUInt16LE(position + 10);
      const compressedSize = cdBuffer.readUInt32LE(position + 20);
      const uncompressedSize = cdBuffer.readUInt32LE(position + 24);
      const nameLength = cdBuffer.readUInt16LE(position + 28);
      const extraLength = cdBuffer.readUInt16LE(position + 30);
      const commentLength = cdBuffer.readUInt16LE(position + 32);
      const localHeaderOffset = cdBuffer.readUInt32LE(position + 42);
      const rawName = cdBuffer.subarray(position + 46, position + 46 + nameLength).toString("utf8");
      const entryName = normalizeEntryName(rawName);
      if (entryName === null) {
        throw new Error(`unsafe zip entry name: ${JSON.stringify(rawName)}`);
      }
      if (uncompressedSize > MAX_ENTRY_BYTES) {
        throw new Error(`zip entry exceeds size limit: ${entryName}`);
      }
      this.entries.set(entryName, {
        method,
        compressedSize,
        uncompressedSize,
        localHeaderOffset,
      });
      position += 46 + nameLength + extraLength + commentLength;
    }
  }

  async readUInt32At(offset) {
    const buffer = Buffer.alloc(4);
    await this.handle.read(buffer, 0, 4, offset);
    return buffer.readUInt32LE(0);
  }

  async readText(entryName) {
    const normalized = normalizeEntryName(entryName);
    if (!normalized) return null;
    await this.buildIndex();
    const entry = this.entries.get(normalized);
    if (!entry) return null;
    // Local header: 30 bytes fixed + name + extra.
    const nameLen = await this.readUInt16At(entry.localHeaderOffset + 26);
    const extraLen = await this.readUInt16At(entry.localHeaderOffset + 28);
    const dataOffset = entry.localHeaderOffset + 30 + nameLen + extraLen;
    const compressed = Buffer.alloc(entry.compressedSize);
    await this.handle.read(compressed, 0, entry.compressedSize, dataOffset);

    let data;
    if (entry.method === 0) {
      data = compressed;
    } else if (entry.method === 8) {
      data = await inflateRaw(compressed, entry.uncompressedSize);
    } else {
      throw new Error(`unsupported zip method ${entry.method} for ${entryName}`);
    }
    if (data.length !== entry.uncompressedSize) {
      throw new Error(`zip entry size mismatch for ${entryName}`);
    }
    return data.toString("utf8");
  }

  async readUInt16At(offset) {
    const buffer = Buffer.alloc(2);
    await this.handle.read(buffer, 0, 2, offset);
    return buffer.readUInt16LE(0);
  }

  async close() {
    if (this.closed) return;
    this.closed = true;
    await this.handle.close();
  }
}

/** Deflate (raw, no zlib header) via the built-in zlib module. */
async function inflateRaw(compressed, expectedSize) {
  const { inflateRawSync } = await import("node:zlib");
  try {
    return inflateRawSync(compressed, { maxOutputLength: Math.max(expectedSize, MAX_ENTRY_BYTES) });
  } catch (error) {
    throw new Error(`inflate failed: ${error.message}`);
  }
}

assert.equal(normalizeEntryName("extension/package.json"), "extension/package.json");
assert.equal(normalizeEntryName("../evil"), null);
assert.equal(normalizeEntryName("/abs/path"), null);
assert.equal(normalizeEntryName("C:/evil"), null);
assert.equal(normalizeEntryName("a\\b"), null);

export const testable = { normalizeEntryName, MAX_ENTRY_BYTES };
