#!/usr/bin/env node

// Renders the desktop icon set (see ./icon-set.mjs) from the LeapMux SVG
// sources: one PNG for each entry, plus the ICO that Windows installs.
//
// Usage: node generate-icons.mjs <png-svg> <ico-svg> <mono-svg> <out-dir> <opaque|transparent>
//
// The two SVG sources differ by their corners, and the caller picks which one
// renders the PNGs: macOS masks the app icon itself and needs the square source
// with opaque corners, while Linux and Windows need the rounded source. The
// last argument states which corners the chosen source must produce, so a
// swapped pair fails the build instead of shipping the wrong shape.
//
// <mono-svg> is the tray / menu-bar source, and it is a THIRD argument rather
// than a reuse of either other one because a status icon is a different
// picture: no background plate, black on transparent, and inset. See
// TRAY_ICON_FILES in ./icon-set.mjs.
//
// Requires @resvg/resvg-js (installed with the frontend dependencies).

import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'

import { buildIco } from '../../../scripts/build-ico.mjs'
import { renderPng } from '../../../scripts/render-icon.mjs'
import { ALL_ICON_FILES, ICO_FILE, ICO_SIZE, pixelSize, TRAY_ICON_FILES } from './icon-set.mjs'

const [pngSvgPath, icoSvgPath, monoSvgPath, outDir, corners] = process.argv.slice(2)
if (!pngSvgPath || !icoSvgPath || !monoSvgPath || !outDir || !corners) {
  console.error('Usage: generate-icons.mjs <png-svg> <ico-svg> <mono-svg> <out-dir> <opaque|transparent>')
  process.exit(1)
}
if (corners !== 'opaque' && corners !== 'transparent') {
  console.error(`Corners must be "opaque" or "transparent", not "${corners}"`)
  process.exit(1)
}

const pngSvg = readFileSync(pngSvgPath)
const icoSvg = readFileSync(icoSvgPath)
const monoSvg = readFileSync(monoSvgPath)
const opaqueCorners = corners === 'opaque'

mkdirSync(outDir, { recursive: true })

// Render each distinct size once. `128x128@2x.png` and `256x256.png` are both
// 256 pixels; they differ only by the density that the bundler reads from the
// name, so they share the bytes.
const rendered = new Map()
for (const name of ALL_ICON_FILES) {
  const size = pixelSize(name)
  if (!rendered.has(size)) {
    rendered.set(size, renderPng(pngSvg, size, { opaqueCorners }))
  }
  writeFileSync(join(outDir, name), rendered.get(size))
}

// The ICO always comes from the rounded source: Windows draws the icon as
// authored, with no mask of its own.
writeFileSync(join(outDir, ICO_FILE), buildIco(renderPng(icoSvg, ICO_SIZE), ICO_SIZE))

// The tray icons. Their corners are never checked: a status icon is a
// silhouette on a transparent square, so `opaqueCorners: false` would be true
// of any art that fits the box and says nothing about whether the right source
// was passed. The `source` field is what pins that.
for (const { name, size, source } of TRAY_ICON_FILES) {
  writeFileSync(join(outDir, name), renderPng(source === 'template' ? monoSvg : icoSvg, size))
}

const trayNames = TRAY_ICON_FILES.map(f => f.name).join(', ')
console.log(`Generated ${ALL_ICON_FILES.length} PNGs (${ALL_ICON_FILES.join(', ')}), ${ICO_FILE} (${ICO_SIZE}x${ICO_SIZE}) and ${TRAY_ICON_FILES.length} tray icons (${trayNames}) in ${outDir}`)
