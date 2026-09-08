import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { UNTRUSTED_LINK_ATTRIBUTE } from '~/lib/untrustedLinkClicks'
import { collectFiles, frontendRoot, posixRelative } from '~/test-support/sourceTree'

/*
 * Guard: an anchor that opens a new tab must say whose words it carries.
 *
 * `interceptUntrustedLinkClicks` acts on `data-untrusted-link` and on nothing
 * else, so a surface that renders agent-authored text into an anchor and omits
 * the mark opens with no prompt at all -- silently, and only for that surface.
 * That is the one exposure the opt-in default buys, and this is what keeps it
 * from growing.
 *
 * The mark is opt-IN rather than opt-out because first-party copy is not a
 * deception risk, and `AboutDialog` proves the point: its licence link reads
 * "Functional Source License, Version 1.1, ALv2 Future License" over a
 * leapmux.dev address, which is honest text and a label/address mismatch at
 * once. Under an opt-out default the app would prompt over its own copy.
 *
 * So each `target="_blank"` anchor lands in one of two sets, and a new one
 * fails here until somebody decides which.
 */

/** Surfaces whose anchor text the APP writes. No prompt belongs on these. */
const FIRST_PARTY: ReadonlySet<string> = new Set([
  'src/components/shell/AboutDialog.tsx',
])

/** `target="_blank"` in either quote style, which is what opens a new tab. */
const OPENS_NEW_TAB = /target=(["'])_blank\1/

const srcRoot = join(frontendRoot, 'src')

describe('untrusted anchors', () => {
  it('marks every agent-authored anchor, or names it first-party', () => {
    const unmarked: string[] = []
    const files = collectFiles(srcRoot, { matches: name => name.endsWith('.tsx') && !name.includes('.test.') })
    for (const file of files) {
      const source = readFileSync(file, 'utf-8')
      if (!OPENS_NEW_TAB.test(source))
        continue
      const relative = posixRelative(frontendRoot, file)
      if (FIRST_PARTY.has(relative) || source.includes('UNTRUSTED_LINK_ATTRIBUTE'))
        continue
      unmarked.push(relative)
    }
    expect(
      unmarked,
      'each of these renders a target="_blank" anchor. Add UNTRUSTED_LINK_ATTRIBUTE '
      + 'if an agent wrote its text, or add it to FIRST_PARTY if the app did.',
    ).toEqual([])
  })

  // The walk passes vacuously the day `src/` moves, so pin that it found the
  // surfaces this guard exists for.
  it('sees both sets, so the walk is not empty', () => {
    const files = collectFiles(srcRoot, { matches: name => name.endsWith('.tsx') && !name.includes('.test.') })
    const withAnchors = files
      .filter(file => OPENS_NEW_TAB.test(readFileSync(file, 'utf-8')))
      .map(file => posixRelative(frontendRoot, file))

    expect(withAnchors).toContain('src/components/shell/AboutDialog.tsx')
    expect(withAnchors).toContain('src/components/chat/results/webSearchResults.tsx')
  })

  // Markdown is the bulk of it, and it sets the mark in the hardening pass
  // rather than at any render site -- so no render path can forget it.
  it('is spelled the same way the markdown pipeline writes it', () => {
    const processor = readFileSync(join(srcRoot, 'lib', 'markdownProcessor.ts'), 'utf-8')
    expect(processor).toContain('UNTRUSTED_LINK_ATTRIBUTE')
    expect(UNTRUSTED_LINK_ATTRIBUTE).toBe('data-untrusted-link')
  })
})
