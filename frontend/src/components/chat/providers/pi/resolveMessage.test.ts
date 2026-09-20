import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { resolveMessageForRendering } from '../registry'
import { input } from '../testUtils'
import './plugin'

const fixturePath = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../../../testdata/pi_message_content_conformance.json')
const fixture = JSON.parse(readFileSync(fixturePath, 'utf8')) as {
  cases: Array<{ name: string, original: Record<string, unknown>, supplemental: unknown, expected: Record<string, unknown> }>
}

describe('pi supplemental artifact conformance', () => {
  it('loads a nonempty shared corpus', () => {
    expect(fixture.cases.length).toBeGreaterThan(0)
  })

  it.each(fixture.cases)('$name', ({ original, supplemental, expected }) => {
    const before = JSON.stringify({ original, supplemental })
    const parsed = { ...input(original), supplementalContent: supplemental }
    const resolved = resolveMessageForRendering(parsed, AgentProvider.PI)
    expect(resolved.parentObject).toEqual(expected)
    expect(resolveMessageForRendering(resolved, AgentProvider.PI)).toBe(resolved)
    expect(JSON.stringify({ original, supplemental })).toBe(before)
  })
})
