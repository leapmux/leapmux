#!/usr/bin/env bun

// Checks that a built .deb installs its icons where a launcher finds them.
//
// Usage: bun verify-deb-icons.mjs <deb> [<deb> ...]
//
// icon-set.test.mjs proves that the checked-in config asks for the right sizes.
// This check proves what the package actually carries, which is the half that a
// tauri-bundler change can break on its own: the bundler derives each
// destination from the PNG header plus the `@2x` suffix, so a size that no icon
// theme declares installs silently and leaves the app with no icon (issue #387).
//
// Runs as the last step of `task build-desktop-linux`, after the .deb is
// repacked, so it reads the file that ships. dpkg-deb is already a hard
// requirement of that task.

import { execFileSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'

import { hicolorIconPath, LINUX_ICON_FILES } from './icon-set.mjs'

const RUST_DIR = resolve(import.meta.dirname, '..')
const HICOLOR_PREFIX = 'usr/share/icons/hicolor/'

// Reads the paths out of a `dpkg-deb -c` listing.
//
// Each row is `ls -l` shaped: permissions, owner/group, size, date, time, then
// the path, which dpkg prefixes with `./`. The path is the rest of the row, not
// the last field -- it can hold a space, and a symlink row appends ` -> target`.
// The path group starts with `\S` so that the separator before it stays
// unambiguous, which also keeps the match linear.
const LISTING_ROW = /^(\S+) +\S+ +\d+ +\S+ +\S+ +(\S.*)$/

export function parseDebPaths(listing) {
  const paths = new Set()
  for (const line of listing.split('\n')) {
    const row = LISTING_ROW.exec(line)
    if (!row) {
      continue
    }
    const [, mode, field] = row
    const path = mode.startsWith('l') ? field.replace(/ -> .*$/, '') : field
    const relative = path.replace(/^\.\//, '')
    if (relative !== '') {
      paths.add(relative)
    }
  }
  return paths
}

// Returns one message for each icon problem in `paths`, and an empty array when
// the package is correct.
export function iconProblems(paths, { icons, desktopEntry }) {
  const problems = []
  for (const icon of icons) {
    if (!paths.has(icon)) {
      problems.push(`missing ${icon}`)
    }
  }
  if (!paths.has(desktopEntry)) {
    problems.push(`missing ${desktopEntry}`)
  }

  // An extra icon is a defect too, not clutter: it means the bundler resolved a
  // size differently than icon-set.mjs states, and the same drift can move the
  // icons that a launcher needs.
  for (const path of paths) {
    if (path.startsWith(HICOLOR_PREFIX) && path.endsWith('.png') && !icons.includes(path)) {
      problems.push(`unexpected icon ${path}`)
    }
  }
  return problems
}

// Reads the names that the .deb is built from. The bundler names each icon file
// after mainBinaryName and the desktop entry after productName, and the desktop
// entry's Icon key is what a launcher looks up -- so the Icon key must be the
// icon file's base name, or the lookup misses every icon the package installs.
export function expectedContents(rustDir) {
  const linuxConfig = JSON.parse(readFileSync(join(rustDir, 'tauri.linux.conf.json'), 'utf8'))
  const template = readFileSync(join(rustDir, 'desktop-template.desktop'), 'utf8')
  const iconKey = /^Icon=(.*)$/m.exec(template)?.[1]
  if (iconKey !== linuxConfig.mainBinaryName) {
    throw new Error(
      `desktop-template.desktop has Icon=${iconKey}, but the bundler installs icons as ${linuxConfig.mainBinaryName}.png`,
    )
  }
  return {
    icons: LINUX_ICON_FILES.map(name => hicolorIconPath(name, linuxConfig.mainBinaryName)),
    desktopEntry: `usr/share/applications/${linuxConfig.productName}.desktop`,
  }
}

function main(debPaths) {
  if (debPaths.length === 0) {
    console.error('Usage: verify-deb-icons.mjs <deb> [<deb> ...]')
    process.exit(1)
  }

  let expected
  try {
    expected = expectedContents(RUST_DIR)
  }
  catch (error) {
    console.error(`Icon check failed: ${error.message}`)
    process.exit(1)
  }

  const failures = []
  for (const debPath of debPaths) {
    // A .deb carries the whole app, so the listing runs to thousands of rows.
    const listing = execFileSync('dpkg-deb', ['-c', debPath], {
      encoding: 'utf8',
      maxBuffer: 64 * 1024 * 1024,
    })
    for (const problem of iconProblems(parseDebPaths(listing), expected)) {
      failures.push(`${debPath}: ${problem}`)
    }
  }

  if (failures.length > 0) {
    console.error('Icon check failed:')
    for (const failure of failures) {
      console.error(`  - ${failure}`)
    }
    process.exit(1)
  }

  console.log(`Verified ${expected.icons.length} icons and the desktop entry in ${debPaths.join(', ')}`)
}

if (import.meta.main) {
  main(process.argv.slice(2))
}
