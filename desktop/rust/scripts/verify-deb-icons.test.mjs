// Tests for verify-deb-icons.mjs, run by `bun test` via `task test-scripts`.
//
// The check itself needs a built .deb and dpkg-deb, so only Linux runs it for
// real. These cover the parts that decide the verdict -- the listing parser and
// the comparison -- so a check that reports "verified" while reading the rows
// wrongly cannot pass unnoticed on any platform.

import { mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'bun:test'

import { hicolorIconPath, LINUX_ICON_FILES } from './icon-set.mjs'
import { expectedContents, iconProblems, parseDebPaths } from './verify-deb-icons.mjs'

const RUST_DIR = resolve(import.meta.dirname, '..')

// Rows copied from `dpkg-deb -c` over a real leapmux-desktop .deb, with a
// symlink row and a spaced path added for the shapes dpkg also emits.
const LISTING = `drwxr-xr-x runner/runner         0 2026-08-15 18:56 ./
drwxr-xr-x runner/runner         0 2026-08-15 18:56 ./usr/share/icons/hicolor/
-rw-r--r-- runner/runner     27604 2026-08-15 18:56 ./usr/share/icons/hicolor/256x256@2/apps/leapmux-desktop.png
-rw-r--r-- runner/runner       199 2026-08-15 18:59 ./usr/share/applications/leapmux-desktop.desktop
lrwxrwxrwx runner/runner         0 2026-08-15 18:59 ./usr/lib/leapmux-desktop/libfoo.so -> libfoo.so.1
-rw-r--r-- runner/runner       512 2026-08-15 18:59 ./usr/share/doc/leapmux-desktop/read me.txt
`

// The layout that shipped as issue #387: one icon, at a size no theme declares.
function brokenPaths() {
  return new Set([
    'usr/share/applications/leapmux-desktop.desktop',
    'usr/share/icons/hicolor/1024x1024@2/apps/leapmux-desktop.png',
  ])
}

function correctPaths() {
  return new Set([
    'usr/share/applications/leapmux-desktop.desktop',
    ...LINUX_ICON_FILES.map(name => hicolorIconPath(name, 'leapmux-desktop')),
  ])
}

const EXPECTED = {
  icons: LINUX_ICON_FILES.map(name => hicolorIconPath(name, 'leapmux-desktop')),
  desktopEntry: 'usr/share/applications/leapmux-desktop.desktop',
}

describe('parseDebPaths', () => {
  const paths = parseDebPaths(LISTING)

  it('strips the ./ prefix that dpkg puts on every row', () => {
    expect(paths).toContain('usr/share/icons/hicolor/256x256@2/apps/leapmux-desktop.png')
    expect(paths).toContain('usr/share/applications/leapmux-desktop.desktop')
  })

  it('takes the link path from a symlink row, not the target', () => {
    expect(paths).toContain('usr/lib/leapmux-desktop/libfoo.so')
    expect(paths).not.toContain('libfoo.so.1')
  })

  it('keeps a path that holds a space whole', () => {
    expect(paths).toContain('usr/share/doc/leapmux-desktop/read me.txt')
  })

  it('drops the package root and the trailing blank line', () => {
    expect(paths).not.toContain('')
    expect(paths.size).toBe(5)
  })
})

describe('iconProblems', () => {
  it('reports nothing when the package carries the whole set', () => {
    expect(iconProblems(correctPaths(), EXPECTED)).toEqual([])
  })

  // The regression check for issue #387, stated as the verifier sees it.
  it('reports every missing icon and the icon in the undeclared directory', () => {
    const problems = iconProblems(brokenPaths(), EXPECTED)
    expect(problems).toContain('missing usr/share/icons/hicolor/32x32/apps/leapmux-desktop.png')
    expect(problems).toContain('missing usr/share/icons/hicolor/512x512/apps/leapmux-desktop.png')
    expect(problems).toContain('unexpected icon usr/share/icons/hicolor/1024x1024@2/apps/leapmux-desktop.png')
    expect(problems).toHaveLength(LINUX_ICON_FILES.length + 1)
  })

  it('reports a missing desktop entry', () => {
    const paths = correctPaths()
    paths.delete('usr/share/applications/leapmux-desktop.desktop')
    expect(iconProblems(paths, EXPECTED)).toEqual(['missing usr/share/applications/leapmux-desktop.desktop'])
  })

  it('reports an icon whose name does not match the desktop entry Icon key', () => {
    const paths = correctPaths()
    paths.add('usr/share/icons/hicolor/32x32/apps/leapmux.png')
    expect(iconProblems(paths, EXPECTED)).toEqual(['unexpected icon usr/share/icons/hicolor/32x32/apps/leapmux.png'])
  })

  it('ignores a non-icon file that sits outside the hicolor tree', () => {
    const paths = correctPaths()
    paths.add('usr/share/doc/leapmux-desktop/changelog.gz')
    expect(iconProblems(paths, EXPECTED)).toEqual([])
  })
})

describe('expectedContents', () => {
  it('derives the icon paths from the checked-in Linux config', () => {
    const expected = expectedContents(RUST_DIR)
    expect(expected.icons).toHaveLength(LINUX_ICON_FILES.length)
    expect(expected.icons).toContain('usr/share/icons/hicolor/32x32/apps/leapmux-desktop.png')
    expect(expected.desktopEntry).toBe('usr/share/applications/leapmux-desktop.desktop')
  })

  it('fails when the desktop entry looks up a name the bundler never writes', () => {
    const dir = mkdtempSync(join(tmpdir(), 'leapmux-icons-'))
    writeFileSync(join(dir, 'tauri.linux.conf.json'), JSON.stringify({
      mainBinaryName: 'leapmux-desktop',
      productName: 'leapmux-desktop',
    }))
    writeFileSync(join(dir, 'desktop-template.desktop'), '[Desktop Entry]\nIcon=leapmux\nName=LeapMux Desktop\n')
    expect(() => expectedContents(dir)).toThrow('Icon=leapmux')
  })
})
