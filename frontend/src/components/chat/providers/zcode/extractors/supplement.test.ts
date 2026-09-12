import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { input } from '../../testUtils'
import { resolveZCodeMessage } from './supplement'

const fixturePath = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../../../../testdata/zcode_message_content_conformance.json')
const fixture = JSON.parse(readFileSync(fixturePath, 'utf8')) as {
  cases: Array<{ name: string, original: Record<string, unknown>, supplemental: unknown, expected: Record<string, unknown> }>
}

describe('zcode supplemental input conformance', () => {
  it('loads a nonempty shared corpus', () => {
    expect(fixture.cases.length).toBeGreaterThan(0)
  })

  it.each(fixture.cases)('$name', ({ original, supplemental, expected }) => {
    const before = JSON.stringify({ original, supplemental })
    expect(resolveZCodeMessage({ ...input(original), supplementalContent: supplemental })).toEqual(expected)
    expect(JSON.stringify({ original, supplemental })).toBe(before)
  })
})
