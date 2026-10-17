import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { ZCODE_QUERY_SOURCE, ZCODE_STOP_REASON } from '~/generated/contracts/zcode-protocol'
import { zcodeIsMainReply } from './messageContent'

/**
 * The browser's half of testdata/zcode_model_response_conformance.json.
 *
 * `TestZCodeModelResponseConformance` in the worker replays the same file against
 * its `zcodeIsMainReply`. A divergence between the two copies persists a side query
 * as an assistant bubble on one side and hides it on the other.
 */
const fixturePath = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../../../testdata/zcode_model_response_conformance.json')
const fixture = JSON.parse(readFileSync(fixturePath, 'utf8')) as {
  cases: Array<{ name: string, payload: Record<string, unknown>, isMainReply: boolean }>
}

describe('zcode model response conformance', () => {
  /**
   * The corpus must pin every branch of the rule. A corpus that loses one lets a
   * divergence on that branch pass both suites: the worker persists a side query
   * the browser hides, or the other way around.
   */
  it('pins every branch of the main-reply rule', () => {
    const cases = fixture.cases
    expect(cases.some(c => c.isMainReply && c.payload.querySource === ZCODE_QUERY_SOURCE.MainTurn), 'no case pins the main turn as the reply').toBe(true)
    expect(cases.some(c => !c.isMainReply && typeof c.payload.querySource === 'string' && c.payload.querySource !== ''), 'no case pins a side source as not the reply').toBe(true)
    expect(cases.some(c => c.isMainReply && c.payload.querySource === undefined), 'no case pins an absent source beside a turn stop reason').toBe(true)
    expect(cases.some(c => !c.isMainReply && c.payload.querySource === undefined && c.payload.stopReason === ZCODE_STOP_REASON.ToolInternal), 'no case pins an absent source beside the tool-internal stop reason').toBe(true)
    expect(cases.some(c => !c.isMainReply && c.payload.querySource === ''), 'no case pins an empty source as not the reply').toBe(true)
    expect(cases.some(c => !c.isMainReply && typeof c.payload.querySource === 'number'), 'no case pins a non-string source as not the reply').toBe(true)
  })

  it.each(fixture.cases)('$name', ({ payload, isMainReply }) => {
    const before = JSON.stringify(payload)
    expect(zcodeIsMainReply(payload)).toBe(isMainReply)
    expect(JSON.stringify(payload)).toBe(before)
  })
})
