import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { input } from '../testUtils'
import { resolveCodexMessage } from './resolveMessage'

/**
 * The browser's half of testdata/codex_message_content_conformance.json.
 *
 * `TestCodexMessageContentConformance` in the worker replays the same file against
 * `codexProvider.ResolveProviderData`. A divergence between the two copies draws a
 * retained command row with its output missing and states nothing about why.
 */
const fixturePath = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../../../testdata/codex_message_content_conformance.json')
const fixture = JSON.parse(readFileSync(fixturePath, 'utf8')) as {
  cases: Array<{ name: string, original: Record<string, unknown>, supplemental: unknown, expected: Record<string, unknown> }>
}

describe('codex supplement resolve conformance', () => {
  it('loads a nonempty shared corpus', () => {
    expect(fixture.cases.length).toBeGreaterThan(0)
  })

  it.each(fixture.cases)('$name', ({ original, supplemental, expected }) => {
    const before = JSON.stringify({ original, supplemental })
    const resolved = resolveCodexMessage({ ...input(original), supplementalContent: supplemental })
    expect(resolved).toEqual(expected)
    // A row the store resolves twice must not grow a second copy of the join.
    expect(resolveCodexMessage({ ...input(resolved ?? {}), supplementalContent: supplemental })).toEqual(expected)
    expect(JSON.stringify({ original, supplemental })).toBe(before)
  })
})
