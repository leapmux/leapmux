import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

/**
 * Cold-load font preloads compete with the entry JS graph on mobile LTE.
 * Only Regular may be preloaded; Bold/Italic load lazily when a code surface
 * needs them. Guard the document shell so a well-meaning re-add does not
 * restore the multi-megabyte blank-screen stall.
 */
describe('entry-server font preloads', () => {
  it('preloads Regular Hack NF only', () => {
    const path = resolve(dirname(fileURLToPath(import.meta.url)), 'entry-server.tsx')
    const src = readFileSync(path, 'utf8')
    expect(src).toContain('HackNerdFont-3.003-Regular.woff2')
    expect(src).not.toContain('HackNerdFont-3.003-Bold.woff2')
    expect(src).not.toContain('HackNerdFont-3.003-Italic.woff2')
    expect(src).not.toContain('HackNerdFont-3.003-BoldItalic.woff2')
    const fontPreloads = [...src.matchAll(/as="font"/g)]
    expect(fontPreloads, 'exactly one as="font" preload').toHaveLength(1)
  })
})
