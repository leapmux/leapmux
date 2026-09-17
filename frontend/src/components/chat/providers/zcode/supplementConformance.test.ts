import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { zcodeEnvelope } from './extractors/toolCommon'
import { zcodeToolSupplement } from './toolSupplement'

/**
 * The browser's half of testdata/zcode_tool_supplement_conformance.json.
 *
 * `TestZCodeToolSupplementConformance` in the worker replays the same file and asserts
 * it WRITES those bytes. Neither side resolves this payload into the frame, so the
 * stored bytes are the whole contract: a key that drifted leaves the worker writing a
 * record the browser then answers as absent, and a retained row draws with no output
 * and nothing to say a record was stored for it.
 */
const fixturePath = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../../../testdata/zcode_tool_supplement_conformance.json')
const fixture = JSON.parse(readFileSync(fixturePath, 'utf8')) as {
  cases: Array<{ name: string, original: Record<string, unknown>, supplement: Record<string, unknown>, expected: Record<string, unknown> }>
}

describe('zcode retained-tool supplement conformance', () => {
  it('loads a nonempty shared corpus', () => {
    expect(fixture.cases.length).toBeGreaterThan(0)
  })

  it.each(fixture.cases)('$name', ({ original, supplement, expected }) => {
    expect(zcodeToolSupplement(supplement)).toEqual(expected)
    // The identity half of the same bytes, which `zcodeNativeTool` reads in production
    // before it touches either payload. The worker writes it from the row's own frame.
    const frame = zcodeEnvelope(original)
    const stored = zcodeEnvelope(supplement)
    expect(stored?.type).toBe(frame?.type)
    expect(stored?.payload.kind).toBe(frame?.payload.kind)
    expect(stored?.payload.toolCallId).toBe(frame?.payload.toolCallId)
  })

  it('answers a row that carries no supplement with nothing', () => {
    expect(zcodeToolSupplement(undefined)).toEqual({ nativeTool: undefined, artifacts: undefined })
    expect(zcodeToolSupplement({ type: 'tool.updated' })).toEqual({ nativeTool: undefined, artifacts: undefined })
  })
})
