// Tests for icon-corners.mjs, run by `bun test` via `task test-scripts`.
//
// This check is the one thing that stops a generator from rendering the wrong
// SVG source. The two sources differ at four pixels; everything else about the
// icon -- the colors, the glyph, the size -- looks correct either way, in the
// build log and in a quick look at the file.

import { describe, expect, it } from 'bun:test'

import { assertCornerAlpha } from './icon-corners.mjs'

// Builds an RGBA buffer of opaque pixels, then sets the four corner alphas in
// the order [top left, top right, bottom left, bottom right].
function pixelsWithCornerAlpha(width, height, [topLeft, topRight, bottomLeft, bottomRight]) {
  const pixels = new Uint8Array(width * height * 4).fill(255)
  pixels[3] = topLeft
  pixels[(width - 1) * 4 + 3] = topRight
  pixels[(height - 1) * width * 4 + 3] = bottomLeft
  pixels[((height - 1) * width + width - 1) * 4 + 3] = bottomRight
  return pixels
}

describe('assertCornerAlpha', () => {
  it('accepts four opaque corners when it expects opaque', () => {
    expect(() => assertCornerAlpha(pixelsWithCornerAlpha(4, 4, [255, 255, 255, 255]), 4, 4, true)).not.toThrow()
  })

  it('accepts four transparent corners when it expects transparent', () => {
    const pixels = pixelsWithCornerAlpha(4, 4, [0, 0, 0, 0])
    expect(() => assertCornerAlpha(pixels, 4, 4, false)).not.toThrow()
  })

  it('rejects opaque corners when it expects transparent', () => {
    const pixels = pixelsWithCornerAlpha(4, 4, [255, 255, 255, 255])
    expect(() => assertCornerAlpha(pixels, 4, 4, false)).toThrow('expected transparent corners')
  })

  it('rejects transparent corners when it expects opaque', () => {
    const pixels = pixelsWithCornerAlpha(4, 4, [0, 0, 0, 0])
    expect(() => assertCornerAlpha(pixels, 4, 4, true)).toThrow('expected opaque corners')
  })

  // Each corner has its own offset, so a check that got one offset wrong would
  // still pass on an image that is opaque or transparent everywhere.
  it('reads each of the four corners', () => {
    const corners = [
      { alpha: [0, 255, 255, 255], at: '(0,0)' },
      { alpha: [255, 0, 255, 255], at: '(3,0)' },
      { alpha: [255, 255, 0, 255], at: '(0,3)' },
      { alpha: [255, 255, 255, 0], at: '(3,3)' },
    ]
    for (const { alpha, at } of corners) {
      expect(() => assertCornerAlpha(pixelsWithCornerAlpha(4, 4, alpha), 4, 4, true))
        .toThrow(`Icon 4x4 has alpha=0 at ${at}; expected opaque corners`)
    }
  })

  // Anti-aliasing leaves a partly covered corner, which is neither state.
  it('rejects a corner that is neither fully opaque nor fully transparent', () => {
    expect(() => assertCornerAlpha(pixelsWithCornerAlpha(4, 4, [128, 255, 255, 255]), 4, 4, true))
      .toThrow('alpha=128')
    expect(() => assertCornerAlpha(pixelsWithCornerAlpha(4, 4, [1, 0, 0, 0]), 4, 4, false))
      .toThrow('alpha=1')
  })

  it('reads a non-square image at its own width and height', () => {
    const pixels = pixelsWithCornerAlpha(8, 2, [255, 255, 255, 0])
    expect(() => assertCornerAlpha(pixels, 8, 2, true)).toThrow('Icon 8x2 has alpha=0 at (7,1)')
  })

  it('reads the corners of a one pixel image without leaving the buffer', () => {
    expect(() => assertCornerAlpha(new Uint8Array([0, 0, 0, 255]), 1, 1, true)).not.toThrow()
    expect(() => assertCornerAlpha(new Uint8Array([0, 0, 0, 0]), 1, 1, true)).toThrow('alpha=0 at (0,0)')
  })
})
