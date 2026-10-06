import { describe, expect, it } from 'vitest'
import { deepseekHarnessCanonicalMcpProjection } from './mcpToolResult'

const value = { contentMatches: true, echoedCount: 0, nextCount: 1, enabled: false, textMatches: true, textCharacters: 70000, nullable: null, hasNullable: true, hasPrivateMeta: false }
function result(projection: unknown = value, callId = 'call') {
  return { type: 'tool/result', data: { message: { toolCallId: callId, isError: false, content: [{ type: 'text', text: JSON.stringify(projection) }] } } }
}

describe('deepseekHarnessCanonicalMcpProjection', () => {
  it('reads actual native projection fields with zero, false, and null preserved', () => {
    const frame = result()
    const before = JSON.stringify(frame)
    expect(deepseekHarnessCanonicalMcpProjection(frame, 'call')).toEqual(value)
    expect(JSON.stringify(frame)).toBe(before)
  })

  it('refuses another call and absent or malformed canonical fields', () => {
    expect(() => deepseekHarnessCanonicalMcpProjection(result(), 'foreign')).toThrow('exact successful')
    for (const projection of [null, false, {}, { ...value, nullable: 0 }, { ...value, nextCount: '1' }, { ...value, textCharacters: -1 }])
      expect(() => deepseekHarnessCanonicalMcpProjection(result(projection), 'call')).toThrow('structured fields')
  })

  it('keeps contradictory native projection flags visible to the caller', () => {
    expect(deepseekHarnessCanonicalMcpProjection(result({ ...value, contentMatches: false, hasPrivateMeta: true }), 'call')).toMatchObject({ contentMatches: false, hasPrivateMeta: true })
  })
})
