#!/usr/bin/env node

// Renders the desktop icon set (see ./icon-set.mjs) from the LeapMux SVG
// sources: one PNG for each entry, plus the ICO that Windows installs.
//
// Usage: node generate-icons.mjs <png-svg> <ico-svg> <out-dir> <opaque|transparent>
//
// The two SVG sources differ by their corners, and the caller picks which one
// renders the PNGs: macOS masks the app icon itself and needs the square source
// with opaque corners, while Linux and Windows need the rounded source. The
// fourth argument states which corners the chosen source must produce, so a
// swapped pair fails the build instead of shipping the wrong shape.
//
// Requires @resvg/resvg-js (installed with the frontend dependencies).

import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'

import { buildIco } from '../../../scripts/build-ico.mjs'
import { renderPng } from '../../../scripts/render-icon.mjs'
import { ALL_ICON_FILES, ICO_FILE, ICO_SIZE, pixelSize } from './icon-set.mjs'

const [pngSvgPath, icoSvgPath, outDir, corners] = process.argv.slice(2)
if (!pngSvgPath || !icoSvgPath || !outDir || !corners) {
  console.error('Usage: generate-icons.mjs <png-svg> <ico-svg> <out-dir> <opaque|transparent>')
  process.exit(1)
}
if (corners !== 'opaque' && corners !== 'transparent') {
  console.error(`Corners must be "opaque" or "transparent", not "${corners}"`)
  process.exit(1)
}

const pngSvg = readFileSync(pngSvgPath)
const icoSvg = readFileSync(icoSvgPath)
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

console.log(`Generated ${ALL_ICON_FILES.length} PNGs (${ALL_ICON_FILES.join(', ')}) and ${ICO_FILE} (${ICO_SIZE}x${ICO_SIZE}) in ${outDir}`)
