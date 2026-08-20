import { readFileSync } from 'node:fs'
import { dirname, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { collectStyleFiles } from '~/test-support/styleFiles'

// A focus ring is drawn from `:focus-visible`, never from `:focus`.
//
// The two differ by INPUT MODALITY, and only on the elements a ring is for.
// `:focus` matches however focus arrived, so a ring drawn from it appears when
// the user clicks -- a mouse user is told where the keyboard is, which is not a
// question they asked. `:focus-visible` matches when the browser judges the
// ring useful: after a keypress, and never after a click on a button.
//
// The mistake is a copy, not a typo. Oat styles its text fields with
// `:where(input:not(...),textarea,select):focus`, and a dropdown trigger that
// wants to LOOK like a field copies that rule onto a `<button>` -- where the
// same selector now means something different. `AgentProviderSelector`'s
// trigger and `PreferencesNav`'s section picker both did, so clicking either
// menu open drew a ring around the button behind it.
//
// NO EXCEPTION IS NEEDED, including for the text fields the rule was copied
// from. A focused text field always matches `:focus-visible` -- verified in
// both engines the app ships on, Chromium and WebKit, where a mouse-clicked
// `input` and `textarea` match it and a `button` and a checkbox do not. So a
// field converted to `:focus-visible` keeps the ring it draws today, and this
// guard can be absolute rather than carry an allowlist that a reader has to
// argue with.

const srcRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')

/**
 * A style-object key that selects `:focus` but not `:focus-visible` or
 * `:focus-within`.
 *
 * Both spellings a `.css.ts` uses are covered: the top-level `':focus'`
 * shorthand and the `'&:focus'` form inside `selectors`. A grouped key
 * (`'&:hover, &:focus'`) matches too, because the group applies every
 * declaration to the `:focus` half as well.
 */
const FOCUS_KEY = /(['"])((?:[^'"]*[\s,])?&?:focus(?![\w-])[^'"]*)\1\s*:\s*\{/g

/** The declarations that draw a ring rather than suppress one. */
const RING_DECLARATION = /\b(outline|outlineColor|outlineWidth|outlineStyle|boxShadow|border|borderColor|borderWidth)\s*:\s*(['`][^'`]*['`])/g

/** The body of the object literal that starts at `open` (the index of its `{`). */
function blockAt(source: string, open: number): string {
  let depth = 0
  for (let i = open; i < source.length; i++) {
    if (source[i] === '{')
      depth++
    else if (source[i] === '}' && --depth === 0)
      return source.slice(open + 1, i)
  }
  return source.slice(open + 1)
}

/** The `export const NAME` a match sits under, so a failure names the style. */
function enclosingStyle(source: string, index: number): string {
  const before = source.slice(0, index)
  const match = [...before.matchAll(/^export const (\w+)/gm)].at(-1)
  return match?.[1] ?? '(module scope)'
}

describe('focus rings are drawn from :focus-visible', () => {
  it('draws no ring from a plain :focus selector', () => {
    const offenders: string[] = []
    for (const file of collectStyleFiles(srcRoot)) {
      const source = readFileSync(file, 'utf8')
      for (const key of source.matchAll(FOCUS_KEY)) {
        const body = blockAt(source, key.index + key[0].length - 1)
        // `outline: 'none'` SUPPRESSES a ring, so a block holding only that is
        // not what this guard is for -- flagging it would push an author
        // toward deleting the suppression rather than toward the right
        // selector.
        const drawn = [...body.matchAll(RING_DECLARATION)]
          .filter(d => d[2]!.slice(1, -1).trim() !== 'none')
        if (drawn.length === 0)
          continue
        offenders.push(
          `${relative(srcRoot, file)} ${enclosingStyle(source, key.index)} `
          + `'${key[2]}' draws ${drawn.map(d => d[1]).join(', ')}`,
        )
      }
    }
    expect(
      offenders,
      'these draw a focus ring that a MOUSE click shows too; select :focus-visible instead:\n  '
      + `${offenders.join('\n  ')}`,
    ).toEqual([])
  })

  it('finds the files it is meant to be guarding', () => {
    // Without this the case above passes vacuously the day the glob or the
    // pattern stops matching, which is exactly when it needs to fail.
    expect(collectStyleFiles(srcRoot).length, 'no .css.ts found -- has the layout moved?')
      .toBeGreaterThanOrEqual(20)
  })

  it('recognises the shapes a .css.ts writes a focus selector in', () => {
    // The guard above is a regex over source text, so a spelling it cannot see
    // is a hole that reads as a pass. Pin both forms, the grouped key, and the
    // two it must NOT claim.
    const seen = (src: string) => [...src.matchAll(FOCUS_KEY)].map(m => m[2])
    expect(seen(`':focus': {`)).toEqual([':focus'])
    expect(seen(`'&:focus': {`)).toEqual(['&:focus'])
    expect(seen(`'&:hover, &:focus': {`)).toEqual(['&:hover, &:focus'])
    expect(seen(`'&:focus-visible': {`)).toEqual([])
    expect(seen(`'&:focus-within': {`)).toEqual([])
  })
})
