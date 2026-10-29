import { existsSync, readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { BOOT_DEFER_CSS_ATTR } from '~/lib/bootDocumentAssets'

function linkTags(html: string): string[] {
  return [
    ...html.matchAll(/<link\b[^>]*>/g),
  ].map(m => m[0])
}

function hrefOf(tag: string): string | undefined {
  return tag.match(/\bhref="([^"]*)"/)?.[1]
}

/**
 * Pins the cold-start asset contract on the last SPA build output. Skips when
 * `.output/public/index.html` is absent (unit-only CI jobs). `task build`
 * regenerates it before embed / e2e.
 */
describe('built index.html cold-start assets', () => {
  const indexPath = resolve(
    dirname(fileURLToPath(import.meta.url)),
    '../.output/public/index.html',
  )
  const hasBuild = existsSync(indexPath)

  it.skipIf(!hasBuild)('defers app stylesheets until after splash paint', () => {
    const html = readFileSync(indexPath, 'utf8')
    const stylesheets = linkTags(html).filter(tag => tag.includes('rel="stylesheet"'))

    expect(stylesheets.length).toBeGreaterThan(0)
    // StartServer's useAssets would append a second, blocking copy of each
    // stylesheet beside the deferred one.
    const hrefs = stylesheets.map(hrefOf)
    expect(new Set(hrefs).size, `a stylesheet is linked twice: ${hrefs.join(', ')}`).toBe(hrefs.length)
    for (const tag of stylesheets) {
      expect(tag).toContain('media="print"')
      expect(tag).toContain(BOOT_DEFER_CSS_ATTR)
      expect(tag).not.toContain('fetchPriority')
    }
    expect(html).toContain(`link[${BOOT_DEFER_CSS_ATTR}]`)
    expect(html).toContain('media="all"')
  })

  it.skipIf(!hasBuild)('modulepreloads only the client entry chunk', () => {
    const html = readFileSync(indexPath, 'utf8')
    const preloads = linkTags(html).filter(tag => tag.includes('rel="modulepreload"'))

    expect(preloads).toHaveLength(1)
    // The preload must fetch the same file that the entry script runs. This
    // comparison holds whatever name the framework gives the entry chunk.
    const entryScripts = [
      ...html.matchAll(/<script type="module" src="([^"]+)"/g),
    ].map(m => m[1])
    expect(entryScripts).toHaveLength(1)
    expect(hrefOf(preloads[0]!)).toBe(entryScripts[0])
    expect(preloads[0]).not.toContain('AuthContext')
    expect(preloads[0]).not.toContain('captcha')
  })

  it.skipIf(!hasBuild)('emits minified splash CSS without source comments', () => {
    const html = readFileSync(indexPath, 'utf8')
    const style = html.match(/<style>([\s\S]*?)<\/style>/)
    expect(style?.[1]).toBeTruthy()
    const css = style![1]!
    expect(css).not.toContain('/*')
    expect(css).toContain('#boot-splash')
    // Readable source is ~14 KB; minified must stay well under that.
    expect(css.length).toBeLessThan(12_000)
  })
})
