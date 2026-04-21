#!/usr/bin/env node

// Converts separate LeapMux SVG sources to PNG (appicon) and ICO (Windows).
// Usage: node generate-icons.mjs <png-svg-path> <ico-svg-path> <png-out> <ico-out> [opaque|transparent]
//
// Requires @resvg/resvg-js (installed automatically when run via bunx).

import { readFileSync, writeFileSync } from 'node:fs'
import { Resvg } from '@resvg/resvg-js'
import { buildIco } from '../../../scripts/build-ico.mjs'

const [pngSvgPath, icoSvgPath, pngOut, icoOut, appiconCorners] = process.argv.slice(2)
if (!pngSvgPath || !icoSvgPath || !pngOut || !icoOut) {
  console.error('Usage: generate-icons.mjs <png-svg> <ico-svg> <png-out> <ico-out> [opaque|transparent]')
  process.exit(1)
}

const pngSvg = readFileSync(pngSvgPath)
const icoSvg = readFileSync(icoSvgPath)

// Render 1024x1024 PNG for appicon.
const appIcon = new Resvg(pngSvg, { fitTo: { mode: 'width', value: 1024 } })
const appIconRendered = appIcon.render()
if (appiconCorners) {
  const shouldBeOpaque = appiconCorners === 'opaque'
  for (const [x, y] of [[0, 0], [appIconRendered.width - 1, 0], [0, appIconRendered.height - 1], [appIconRendered.width - 1, appIconRendered.height - 1]]) {
    const alpha = appIconRendered.pixels[(y * appIconRendered.width + x) * 4 + 3]
    const ok = shouldBeOpaque ? alpha === 255 : alpha === 0
    if (!ok) {
      const expected = shouldBeOpaque ? 'opaque' : 'transparent'
      throw new Error(`${pngOut} has alpha=${alpha} at (${x},${y}); expected ${expected} corners`)
    }
  }
}
writeFileSync(pngOut, appIconRendered.asPng())

// Render 256x256 PNG, then wrap in ICO for Windows.
const icoIcon = new Resvg(icoSvg, { fitTo: { mode: 'width', value: 256 } })
writeFileSync(icoOut, buildIco(icoIcon.render().asPng(), 256))

console.log(`Generated ${pngOut} (1024x1024) and ${icoOut} (256x256 ICO)`)
