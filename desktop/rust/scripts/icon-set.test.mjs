// Tests for icon-set.mjs, run by `bun test` via `task test-scripts`.
//
// The risk these cover is drift, not arithmetic. The icon set, the two Tauri
// configs, and the desktop entry template each hold one part of the same fact,
// and nothing at build time complains when they disagree: tauri-bundler
// installs whatever size it is given, into a directory it derives from the PNG
// header, and a launcher that cannot find the icon says nothing either.

import { readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'bun:test'

import {
  ALL_ICON_FILES,
  hicolorIconPath,
  ICO_FILE,
  LINUX_ICON_FILES,
  pixelSize,
  SHARED_ICON_FILES,
  TRAY_ICON_FILES,
} from './icon-set.mjs'

const RUST_DIR = resolve(import.meta.dirname, '..')

// The icon sizes that the hicolor theme declares in its index.theme, taken from
// the `Directories=` key of the freedesktop default-icon-theme source. Each one
// exists twice there, as `<size>x<size>/apps` and as `<size>x<size>@2/apps`.
// Nothing outside this list is reachable: an icon lookup reads index.theme, so a
// directory that index.theme omits is invisible even though the file is on disk.
const HICOLOR_SIZES = [16, 22, 24, 32, 36, 48, 64, 72, 96, 128, 192, 256, 512]

function readJson(name) {
  return JSON.parse(readFileSync(join(RUST_DIR, name), 'utf8'))
}

const baseConfig = readJson('tauri.conf.json')
const linuxConfig = readJson('tauri.linux.conf.json')

describe('pixelSize', () => {
  it('reads the size from a plain name', () => {
    expect(pixelSize('32x32.png')).toBe(32)
    expect(pixelSize('512x512.png')).toBe(512)
  })

  it('doubles the size for a @2x name', () => {
    expect(pixelSize('128x128@2x.png')).toBe(256)
    expect(pixelSize('512x512@2x.png')).toBe(1024)
  })

  it('rejects a name that states no size', () => {
    expect(() => pixelSize('icon.png')).toThrow('does not have the form')
    expect(() => pixelSize('128x128@3x.png')).toThrow('does not have the form')
    expect(() => pixelSize('128x128.ico')).toThrow('does not have the form')
  })

  it('rejects a name that is not square', () => {
    expect(() => pixelSize('128x64.png')).toThrow('is not square')
  })
})

describe('hicolorIconPath', () => {
  it('uses the plain directory for a standard density icon', () => {
    expect(hicolorIconPath('256x256.png', 'leapmux-desktop'))
      .toBe('usr/share/icons/hicolor/256x256/apps/leapmux-desktop.png')
  })

  // The bundler takes the directory's size from the PNG header and the `@2`
  // suffix from the file name, so a 256 pixel @2x file lands under 256x256@2 --
  // NOT under the 128x128@2 that its name suggests.
  it('uses the pixel size and the @2 suffix for a high density icon', () => {
    expect(hicolorIconPath('128x128@2x.png', 'leapmux-desktop'))
      .toBe('usr/share/icons/hicolor/256x256@2/apps/leapmux-desktop.png')
  })
})

describe('LINUX_ICON_FILES', () => {
  // The regression test for issue #387. The package shipped one 1024x1024 icon
  // named icon@2x.png, so it installed to hicolor/1024x1024@2/apps/ and no
  // launcher ever found it.
  it('holds only sizes that the hicolor theme declares', () => {
    for (const name of LINUX_ICON_FILES) {
      const pixels = pixelSize(name)
      expect(HICOLOR_SIZES).toContain(pixels)
    }
  })

  // tauri-codegen embeds the first PNG as the window icon, which GTK publishes
  // as _NET_WM_ICON. A panel scales 128 down cleanly; 32 would blur when a 48
  // pixel panel scales it up, and the 1024 master would cost 4 MiB of binary.
  it('starts with the icon that a panel scales best', () => {
    expect(LINUX_ICON_FILES[0]).toBe('128x128.png')
  })

  it('covers the sizes a launcher asks for most', () => {
    expect(LINUX_ICON_FILES.map(pixelSize)).toEqual(expect.arrayContaining([32, 48, 64, 128, 256, 512]))
  })

  it('matches bundle.icon in tauri.linux.conf.json', () => {
    expect(linuxConfig.bundle.icon).toEqual(LINUX_ICON_FILES.map(name => `icons/${name}`))
  })
})

describe('SHARED_ICON_FILES', () => {
  // icns::IconType::from_pixel_size_and_density maps powers of two only. The
  // macOS bundler resizes anything else down to the next power of two, which
  // adds a duplicate of a size the set already has.
  it('holds power of two sizes only', () => {
    for (const name of SHARED_ICON_FILES) {
      const pixels = pixelSize(name)
      expect(Number.isInteger(Math.log2(pixels))).toBe(true)
    }
  })

  // `cargo tauri dev` on macOS has no .app bundle to take a Dock icon from, so
  // tauri-codegen embeds the first PNG for it. The master keeps that icon sharp.
  it('starts with the 1024 pixel master that the macOS Dock needs at scale 2', () => {
    expect(SHARED_ICON_FILES[0]).toBe('512x512@2x.png')
    expect(pixelSize(SHARED_ICON_FILES[0])).toBe(1024)
  })

  it('matches bundle.icon in tauri.conf.json, with the ICO last', () => {
    expect(baseConfig.bundle.icon).toEqual([
      ...SHARED_ICON_FILES.map(name => `icons/${name}`),
      `icons/${ICO_FILE}`,
    ])
  })
})

describe('ALL_ICON_FILES', () => {
  it('holds each file of both platform lists exactly once', () => {
    expect([...new Set(ALL_ICON_FILES)]).toEqual(ALL_ICON_FILES)
    for (const name of [...LINUX_ICON_FILES, ...SHARED_ICON_FILES]) {
      expect(ALL_ICON_FILES).toContain(name)
    }
  })

  it('adds no file that neither platform ships', () => {
    const shipped = new Set([...LINUX_ICON_FILES, ...SHARED_ICON_FILES])
    for (const name of ALL_ICON_FILES) {
      expect(shipped.has(name)).toBe(true)
    }
  })
})

describe('TRAY_ICON_FILES', () => {
  // The tray icons are embedded by `tauri::include_image!`, never bundled. If
  // one reached `bundle.icon` the Linux bundler would install a 32 pixel
  // silhouette into the hicolor theme as the APP icon, and every launcher
  // would show it -- a failure that no build step reports, exactly like the
  // undeclared-directory case the LINUX_ICON_FILES comment describes.
  it('ships in neither bundler list', () => {
    const bundled = new Set([...ALL_ICON_FILES, ICO_FILE])
    for (const { name } of TRAY_ICON_FILES) {
      expect(bundled.has(name)).toBe(false)
    }
  })

  it('appears in neither config bundle.icon', () => {
    const declared = new Set([...baseConfig.bundle.icon, ...linuxConfig.bundle.icon])
    for (const { name } of TRAY_ICON_FILES) {
      expect(declared.has(`icons/${name}`)).toBe(false)
      expect(declared.has(name)).toBe(false)
    }
  })

  it('renders the macOS template at exactly twice the 18 point status height', () => {
    // tray-icon forces the NSImage to 18 points, so anything but 36 pixels is
    // resampled on a Retina display.
    const template = TRAY_ICON_FILES.find(f => f.source === 'template')
    expect(template).toBeDefined()
    expect(template.size).toBe(36)
  })

  it('gives every entry a unique PNG name and a known source', () => {
    const names = TRAY_ICON_FILES.map(f => f.name)
    expect(new Set(names).size).toBe(names.length)
    for (const { name, size, source } of TRAY_ICON_FILES) {
      expect(name.endsWith('.png')).toBe(true)
      expect(Number.isInteger(size) && size > 0).toBe(true)
      expect(['colour', 'template']).toContain(source)
    }
  })

  it('covers both platform families', () => {
    // One template for macOS and one colour icon for Linux and Windows. A
    // missing source means a platform silently falls back to the app icon.
    const sources = new Set(TRAY_ICON_FILES.map(f => f.source))
    expect(sources).toEqual(new Set(['colour', 'template']))
  })
})

describe('desktop entry', () => {
  // The bundler names each icon file after mainBinaryName. A launcher resolves
  // the Icon key against those file names, so the two must agree.
  it('looks the icon up by the name the bundler writes', () => {
    const template = readFileSync(join(RUST_DIR, 'desktop-template.desktop'), 'utf8')
    const iconKey = /^Icon=(.*)$/m.exec(template)?.[1]
    expect(iconKey).toBe(linuxConfig.mainBinaryName)
  })
})
