import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { input } from '../testUtils'
import { resolveACPMessage } from './extractors/toolCall'

/**
 * The browser's half of testdata/acp_message_content_conformance.json.
 *
 * `TestACPMessageContentConformance` in the worker replays the same file against
 * `resolveACPMessageContent`. Six providers share this resolve, so a divergence
 * between the two copies shows the reader a different row than the worker's own
 * semantic extractors saw -- and neither side fails, because each one is internally
 * consistent.
 */
const fixturePath = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../../../testdata/acp_message_content_conformance.json')
const fixture = JSON.parse(readFileSync(fixturePath, 'utf8')) as {
  cases: Array<{ name: string, original: Record<string, unknown>, supplemental: unknown, expected: Record<string, unknown> }>
}

describe('acp supplement resolve conformance', () => {
  it('loads a nonempty shared corpus', () => {
    expect(fixture.cases.length).toBeGreaterThan(0)
  })

  it.each(fixture.cases)('$name', ({ original, supplemental, expected }) => {
    const before = JSON.stringify({ original, supplemental })
    const resolved = resolveACPMessage({ ...input(original), supplementalContent: supplemental })
    expect(resolved).toEqual(expected)
    // A row the store resolves twice must not grow a second copy of the join.
    expect(resolveACPMessage({ ...input(resolved ?? {}), supplementalContent: supplemental })).toEqual(expected)
    expect(JSON.stringify({ original, supplemental })).toBe(before)
  })
})
