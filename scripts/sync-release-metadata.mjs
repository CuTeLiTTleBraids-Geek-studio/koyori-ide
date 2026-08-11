#!/usr/bin/env node

import { readFile, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const checkOnly = process.argv.includes('--check')

function replaceOne(source, pattern, replacement, label) {
  const matches = source.match(new RegExp(pattern.source, pattern.flags.includes('g') ? pattern.flags : `${pattern.flags}g`)) ?? []
  if (matches.length !== 1) {
    throw new Error(`${label}: expected exactly one field, found ${matches.length}`)
  }
  return source.replace(pattern, replacement)
}

function replacePlistString(source, key, value) {
  const escapedKey = key.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const pattern = new RegExp(`(<key>${escapedKey}</key>\\s*<string>)[^<]*(</string>)`)
  return replaceOne(source, pattern, `$1${value}$2`, `build/darwin/Info.plist ${key}`)
}

// SemVer 2.0.0 core plus optional prerelease and build metadata. MSIX cannot
// represent prerelease/build-metadata characters, so those are mapped away
// explicitly (see below); every other consumer keeps the full VERSION string.
const semverRe = /^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$/

const version = (await readFile(path.join(root, 'VERSION'), 'utf8')).trim()
if (!semverRe.test(version)) {
  throw new Error(`VERSION contains an invalid SemVer value: ${version || '<empty>'}`)
}

const semverParts = /^([0-9]+)\.([0-9]+)\.([0-9]+)/.exec(version)
if (!semverParts) throw new Error(`cannot extract numeric SemVer triple from ${version}`)
// MSIX package Identity requires a four-part version. Prerelease/build-metadata
// suffixes cannot appear in MSIX, so the explicit, tested mapping is
// <major>.<minor>.<patch>.0. An unmappable version fails closed above.
const msixVersion = `${semverParts[1]}.${semverParts[2]}.${semverParts[3]}.0`

const updates = []

async function updateText(relativePath, transform) {
  const absolutePath = path.join(root, relativePath)
  const current = await readFile(absolutePath, 'utf8')
  const expected = transform(current)
  if (current === expected) return
  updates.push(relativePath)
  if (!checkOnly) await writeFile(absolutePath, expected, 'utf8')
}

await updateText('build/config.yml', (source) => {
  let result = replaceOne(source, /^  companyName: "[^"]+"([^\n]*)$/m, '  companyName: "Koyori IDE Contributors"$1', 'build/config.yml info.companyName')
  result = replaceOne(result, /^  productName: "[^"]+"([^\n]*)$/m, '  productName: "Koyori IDE"$1', 'build/config.yml info.productName')
  result = replaceOne(result, /^  copyright: "[^"]+"([^\n]*)$/m, '  copyright: "(c) 2026, Koyori IDE Contributors"$1', 'build/config.yml info.copyright')
  return replaceOne(result, /^  version: "[^"]+"([^\n]*)$/m, `  version: "${version}"$1`, 'build/config.yml info.version')
})

await updateText('frontend/package.json', (source) => {
  const parsed = JSON.parse(source)
  parsed.version = version
  return `${JSON.stringify(parsed, null, 2)}\n`
})

await updateText('build/windows/info.json', (source) => {
  const parsed = JSON.parse(source)
  // The string table must use a real language id (en-US 0x0409): language
  // neutral "0000" cannot be matched by Win32 GetFileVersionInfo, so file
  // properties would show empty strings even though the resource exists.
  const table = parsed.info?.['0409']
  if (!parsed.fixed || !table) throw new Error('build/windows/info.json has an unexpected shape (need info.0409)')
  parsed.fixed.file_version = version
  parsed.fixed.product_version = version
  table.ProductVersion = version
  table.CompanyName = 'Koyori IDE Contributors'
  table.LegalCopyright = '© 2026, Koyori IDE Contributors'
  table.ProductName = 'Koyori IDE'
  return `${JSON.stringify(parsed, null, '\t')}\n`
})

// Windows application manifest (wails.exe.manifest): assemblyIdentity version.
await updateText('build/windows/wails.exe.manifest', (source) =>
  replaceOne(source, /^([\s\S]*?<assemblyIdentity[^>]*version=")[^"]*(")/, `$1${version}$2`, 'wails.exe.manifest assemblyIdentity version'),
)

// MSIX app manifest: Identity Version is four-part and cannot carry
// prerelease/build-metadata; map to <major>.<minor>.<patch>.0 (tested).
await updateText('build/windows/msix/app_manifest.xml', (source) =>
  replaceOne(
    replaceOne(
      replaceOne(source, /^([\s\S]*?<Identity[^>]*Version=")[^"]*(")/, `$1${msixVersion}$2`, 'msix app_manifest.xml Identity Version'),
      /(<Properties>[\s\S]*?<DisplayName>)[^<]*(<\/DisplayName>)/,
      '$1Koyori IDE$2',
      'msix app_manifest.xml Properties.DisplayName',
    ),
    /(<uap:VisualElements[\s\S]*?DisplayName=")[^"]*(")/,
    '$1Koyori IDE$2',
    'msix app_manifest.xml VisualElements.DisplayName',
  ),
)

await updateText('build/linux/nfpm/nfpm.yaml', (source) => {
  let result = replaceOne(source, /^version:\s*"[^"]+"$/m, `version: "${version}"`, 'nfpm version')
  result = replaceOne(result, /^vendor:\s*"[^"]+"$/m, 'vendor: "Koyori IDE Contributors"', 'nfpm vendor')
  return replaceOne(result, /^homepage:\s*"[^"]+"$/m, 'homepage: "https://github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide"', 'nfpm homepage')
})

await updateText('build/darwin/Info.plist', (source) => {
  let result = replacePlistString(source, 'CFBundleName', 'Koyori IDE')
  result = replacePlistString(result, 'CFBundleExecutable', 'koyori-ide')
  result = replacePlistString(result, 'CFBundleIdentifier', 'com.koyori.app')
  result = replacePlistString(result, 'CFBundleVersion', version)
  result = replacePlistString(result, 'CFBundleShortVersionString', version)
  return replacePlistString(result, 'NSHumanReadableCopyright', 'Copyright 2026 Koyori IDE Contributors')
})

if (updates.length > 0 && checkOnly) {
  console.error(`Release metadata is not synchronized with VERSION (${version}):`)
  for (const file of updates) console.error(`  - ${file}`)
  process.exitCode = 1
} else if (updates.length > 0) {
  console.log(`Synchronized ${updates.length} release metadata files to ${version}.`)
} else {
  console.log(`Release metadata is synchronized with VERSION (${version}).`)
}
