// The corner check that both icon generators apply to a rendered icon.
//
// It lives apart from ./render-icon.mjs, which imports @resvg/resvg-js from
// frontend/node_modules: `bun test` resolves neither NODE_PATH nor that
// directory, so a test that reaches the renderer cannot run. This half is pure,
// and it is the half that carries the logic.

// Fails when a corner pixel does not have the alpha that the caller expects.
// `pixels` is RGBA, four bytes for each pixel, in row order.
//
// The two LeapMux sources differ at four pixels only: the rounded icon leaves
// its corners transparent, and the square icon fills them. A generator that
// renders the wrong source produces an icon that looks correct everywhere else,
// so each generator states which source it expects and this check enforces it.
export function assertCornerAlpha(pixels, width, height, shouldBeOpaque) {
  for (const [x, y] of [[0, 0], [width - 1, 0], [0, height - 1], [width - 1, height - 1]]) {
    const alpha = pixels[(y * width + x) * 4 + 3]
    const ok = shouldBeOpaque ? alpha === 255 : alpha === 0
    if (!ok) {
      const expected = shouldBeOpaque ? 'opaque' : 'transparent'
      throw new Error(`Icon ${width}x${height} has alpha=${alpha} at (${x},${y}); expected ${expected} corners`)
    }
  }
}
