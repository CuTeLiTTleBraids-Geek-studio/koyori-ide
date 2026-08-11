#!/usr/bin/env node

// P9-G08 AC3 (Windows leg): verify that a packaged koyori-ide.exe carries the
// repository VERSION in its Win32 version resource. Reads the VS_FIXEDFILEINFO
// numeric versions plus the StringFileInfo strings via VerQueryValue, then
// asserts everything matches the VERSION file. This is the same data the
// Explorer Properties dialog displays.

import assert from "node:assert/strict";
import { readFileSync, writeFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const exe = process.argv[2] ? path.resolve(process.argv[2]) : path.join(root, "bin", "koyori-ide.exe");
const version = readFileSync(path.join(root, "VERSION"), "utf8").trim();

const csharp = `
using System;
using System.Runtime.InteropServices;
public static class KoyoriVersionProbe {
  [DllImport("version.dll", CharSet=CharSet.Unicode)] public static extern int GetFileVersionInfoSize(string f, out int h);
  [DllImport("version.dll", CharSet=CharSet.Unicode)] public static extern bool GetFileVersionInfo(string f, int h, int size, byte[] data);
  [DllImport("version.dll", CharSet=CharSet.Unicode)] public static extern bool VerQueryValue(byte[] data, string subBlock, out IntPtr buffer, out uint len);
  private static string Q(byte[] data, string sub) {
    IntPtr buf; uint len;
    if (!VerQueryValue(data, sub, out buf, out len)) return null;
    return Marshal.PtrToStringUni(buf);
  }

  private static string Esc(string s) {
    return (s ?? "").Replace("\\\\", "\\\\\\\\").Replace("\\"", "\\\\\\"");
  }

  public static string Probe(string file) {
    int handle;
    int size = GetFileVersionInfoSize(file, out handle);
    if (size <= 0) return "{\\"error\\":\\"GetFileVersionInfoSize failed\\"}";
    byte[] data = new byte[size];
    if (!GetFileVersionInfo(file, 0, size, data)) return "{\\"error\\":\\"GetFileVersionInfo failed\\"}";
    IntPtr tb; uint tl;
    string lang = "040904B0";
    if (VerQueryValue(data, "\\\\VarFileInfo\\\\Translation", out tb, out tl) && tl >= 4) {
      ushort l = (ushort)Marshal.ReadInt16(tb, 0);
      ushort c = (ushort)Marshal.ReadInt16(tb, 2);
      lang = l.ToString("X4") + c.ToString("X4");
    }
    // VS_FIXEDFILEINFO sits after the VS_VERSION_INFO header (6 bytes) + key
    // "VS_VERSION_INFO\\0" (32 bytes UTF-16), padded to a 4-byte boundary.
    int fixedOff = 40;
    uint fvms = BitConverter.ToUInt32(data, fixedOff + 8);
    uint fvls = BitConverter.ToUInt32(data, fixedOff + 12);
    uint pvms = BitConverter.ToUInt32(data, fixedOff + 16);
    uint pvls = BitConverter.ToUInt32(data, fixedOff + 20);
    string fileVersionRaw = (fvms >> 16) + "." + (fvms & 0xFFFF) + "." + (fvls >> 16) + "." + (fvls & 0xFFFF);
    string productVersionRaw = (pvms >> 16) + "." + (pvms & 0xFFFF) + "." + (pvls >> 16) + "." + (pvls & 0xFFFF);
    return "{\\"fileVersionRaw\\":\\"" + Esc(fileVersionRaw) + "\\",\\"productVersionRaw\\":\\"" + Esc(productVersionRaw) +
      "\\",\\"productVersion\\":\\"" + Esc(Q(data, "\\\\StringFileInfo\\\\" + lang + "\\\\ProductVersion")) +
      "\\",\\"productName\\":\\"" + Esc(Q(data, "\\\\StringFileInfo\\\\" + lang + "\\\\ProductName")) +
      "\\",\\"companyName\\":\\"" + Esc(Q(data, "\\\\StringFileInfo\\\\" + lang + "\\\\CompanyName")) +
      "\\",\\"fileDescription\\":\\"" + Esc(Q(data, "\\\\StringFileInfo\\\\" + lang + "\\\\FileDescription")) + "\\"}";
  }
}
`;

const csPath = path.join(os.tmpdir(), `koyori-versioninfo-${process.pid}.cs`);
writeFileSync(csPath, csharp, "utf8");
const ps = `$ErrorActionPreference='Stop'; Add-Type -Path '${csPath}'; [KoyoriVersionProbe]::Probe($env:KOYORI_IDE_VERSIONINFO_EXE)`;
const result = spawnSync("powershell.exe", ["-NoProfile", "-NonInteractive", "-Command", ps], {
  encoding: "utf8",
  windowsHide: true,
  env: { ...process.env, KOYORI_IDE_VERSIONINFO_EXE: exe },
  timeout: 60_000,
});
try {
  if (result.status !== 0) {
    console.error(result.stderr || result.stdout);
    process.exit(1);
  }
  const info = JSON.parse(result.stdout.trim().split(/\r?\n/).at(-1));
  if (info.error) throw new Error(info.error);

  assert.equal(info.fileVersionRaw, `${version}.0`, `VS_FIXEDFILEINFO FileVersion ${info.fileVersionRaw} != ${version}.0`);
  assert.equal(info.productVersionRaw, `${version}.0`, `VS_FIXEDFILEINFO ProductVersion ${info.productVersionRaw} != ${version}.0`);
  assert.equal(info.productVersion, version, `StringFileInfo ProductVersion ${info.productVersion} != ${version}`);
  assert.equal(info.productName, "Koyori IDE", `ProductName ${info.productName} != Koyori IDE`);
  assert.equal(info.companyName, "Koyori IDE Contributors", `CompanyName ${info.companyName} != Koyori IDE Contributors`);

  console.log(`[windows-versioninfo] OK ${path.relative(root, exe)} FileVersion=${info.fileVersionRaw} ProductVersion=${info.productVersion} ProductName=${info.productName}`);
} finally {
  try { const { rmSync } = await import("node:fs"); rmSync(csPath, { force: true }); } catch { /* ignore */ }
}
