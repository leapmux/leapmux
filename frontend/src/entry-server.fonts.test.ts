import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

/**
 * Cold-load font preloads compete with the entry JS graph on mobile LTE.
 * The LTE tracer ranked even one Regular Hack NF preload as ~half of
 * bytes-before-shell. Faces load lazily via @font-face when code surfaces
 * need them — never via document preload.
 */
describe('entry-server font preloads', () => {
  it('preloads no Hack NF faces', () => {
    const path = resolve(dirname(fileURLToPath(import.meta.url)), 'entry-server.tsx')
    const src = readFileSync(path, 'utf8')
    expect(src).not.toContain('HackNerdFont-3.003-Regular.woff2')
    expect(src).not.toContain('HackNerdFont-3.003-Bold.woff2')
    expect(src).not.toContain('HackNerdFont-3.003-Italic.woff2')
    expect(src).not.toContain('HackNerdFont-3.003-BoldItalic.woff2')
    expect(src.includes('as="font"'), 'no as="font" preload tags').toBe(false)
  })
})

describe('entry-server boot splash polarity', () => {
  it('ships document CSS and the blocking boot script from bootSplashTheme', () => {
    const path = resolve(dirname(fileURLToPath(import.meta.url)), 'entry-server.tsx')
    const src = readFileSync(path, 'utf8')
    expect(src).toContain('bootSplashDocumentCss')
    expect(src).toContain('bootThemeScript')
    expect(src).toContain('BOOT_SPLASH_LABEL')
  })
})
