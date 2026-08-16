// Renders an SVG source to a square PNG, shared by the web icon generator
// (frontend/scripts/generate-icons.mjs) and the desktop icon generator
// (desktop/rust/scripts/generate-icons.mjs).
//
// Requires @resvg/resvg-js, which the repo installs into frontend/node_modules.
// Both callers run under `NODE_PATH=frontend/node_modules bun ...`, so importing
// this module outside that environment fails at load time. The corner check
// lives in ./icon-corners.mjs for that reason -- it stays reachable from a test.

import { Resvg } from '@resvg/resvg-js'

import { assertCornerAlpha } from './icon-corners.mjs'

// Renders `svgData` at `size` physical pixels and returns the PNG bytes.
//
// Pass `opaqueCorners` to assert the corner pixels: `true` for an icon that must
// fill its square (the macOS app icon, which the system masks itself), `false`
// for an icon whose rounded corners must stay transparent. Omit it to skip the
// check.
export function renderPng(svgData, size, { opaqueCorners } = {}) {
  const resvg = new Resvg(svgData, { fitTo: { mode: 'width', value: size } })
  const rendered = resvg.render()
  if (opaqueCorners !== undefined) {
    assertCornerAlpha(rendered.pixels, rendered.width, rendered.height, opaqueCorners)
  }
  return rendered.asPng()
}
