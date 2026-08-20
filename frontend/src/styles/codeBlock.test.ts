import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { describe, expect, it } from 'vitest'
import { codeBlockPre } from '~/styles/codeBlock'

// `codeBlockPre` is applied through an unlayered vanilla-extract class, and Oat
// declares its own `pre` rule inside its `base` layer -- so every value stated
// here wins over Oat's unconditionally, whatever the specificity. That makes a
// restated value a silent override rather than a redundancy, and the radius was
// one: `--radius-small` is what Oat gives INLINE `code`, a third of the
// `--radius-medium` it gives a `<pre>`, so a fenced block was rounded at the
// inline corner.
//
// This reads Oat's own stylesheet rather than repeating the token name, so it
// also fails if a version bump changes what Oat intends.

const require_ = createRequire(import.meta.url)

/** The declarations Oat's base stylesheet attaches to the bare `selector`. */
function oatBaseRule(selector: string): Record<string, string> {
  const path = require_.resolve('@knadh/oat/css/00-base.css')
  const css = readFileSync(path, 'utf8')
  const found: Record<string, string> = {}
  for (const chunk of css.split('}')) {
    const [head, body] = chunk.split('{')
    if (body === undefined || head!.trim().split(/\s*,\s*/).every(s => s.trim() !== selector))
      continue
    for (const decl of body.split(';')) {
      const [prop, ...rest] = decl.split(':')
      if (rest.length)
        found[prop!.trim()] = rest.join(':').trim()
    }
  }
  return found
}

describe('codeBlockPre', () => {
  it('rounds a block at the radius Oat gives a <pre>, not the one it gives inline code', () => {
    const oat = oatBaseRule('pre')
    expect(oat['border-radius'], 'Oat no longer rounds <pre> -- has its base stylesheet moved?').toBeTruthy()
    expect(codeBlockPre('hidden').borderRadius).toBe(oat['border-radius'])
    // ...and that is a different corner from inline code's, which is the whole
    // reason restating it was a bug rather than a no-op.
    expect(oat['border-radius']).not.toBe(oatBaseRule('code')['border-radius'])
  })

  it('moves the scroll and the padding off the <pre> so an overlay can anchor to it', () => {
    // The copy button and the language label are absolutely positioned against
    // the <pre>; if the <pre> scrolled, they would scroll away with the code.
    // Oat puts both the padding and the overflow on the <pre>, so both are
    // overridden here and restated on the <code>.
    expect(oatBaseRule('pre').padding, 'Oat no longer pads <pre>').toBeTruthy()
    expect(codeBlockPre('hidden')).toMatchObject({ padding: 0, position: 'relative', overflowX: 'hidden' })
    expect(codeBlockPre('visible').overflowX).toBe('visible')
  })

  it('leaves a fenced block unbordered', () => {
    // The field is what marks a block. Re-adding an outline is a visual
    // decision, not something a shared <pre> rule should reintroduce.
    expect(codeBlockPre('hidden')).not.toHaveProperty('border')
  })
})
