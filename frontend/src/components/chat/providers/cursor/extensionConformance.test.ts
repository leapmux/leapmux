import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { acpToolSupplement } from '../acp/toolSupplement'
import { cursorExtension } from './extractors/extensions'

/**
 * The browser's half of testdata/cursor_extension_conformance.json.
 *
 * `TestCursorExtensionConformance` in the worker replays the same file against
 * `cursorProvider.ResolveProviderData`. The two sides apply the SAME gate: a `cursor/*`
 * frame arrives one row after the call it describes, and an envelope that does not
 * identify that row reaches neither. While the worker wrote an identity-free envelope the
 * browser's gate refused it and the worker's own resolve took it, so a
 * `cursor/update_todos` row drew as a plain tool call with no checklist.
 */
const fixturePath = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../../../testdata/cursor_extension_conformance.json')
const fixture = JSON.parse(readFileSync(fixturePath, 'utf8')) as {
  cases: Array<{
    name: string
    original: Record<string, unknown>
    supplement: Record<string, unknown>
    extension: { method: string, params: Record<string, unknown> } | null
  }>
}

describe('cursor extension resolve conformance', () => {
  it('loads a nonempty shared corpus', () => {
    expect(fixture.cases.length).toBeGreaterThan(0)
  })

  it.each(fixture.cases)('$name', ({ original, supplement, extension }) => {
    // Through the SAME gate production applies: the plugin reaches the frame only as
    // `facts.extra`, the supplement that matched the row's own identity.
    const read = cursorExtension(acpToolSupplement(original, supplement))
    if (extension === null) {
      expect(read).toBeNull()
      return
    }
    expect(read?.method).toBe(extension.method)
    expect(read?.params).toEqual(extension.params)
  })
})
