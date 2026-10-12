import { describe, expect, it } from 'vitest'
import { isObject } from '~/lib/jsonPick'
import { isLettaToolProgress, lettaReturnedData, lettaToolPayload } from './toolOutput'

describe('isLettaToolProgress', () => {
  it('requires the exact current-window ID and the same native call', () => {
    const progress = { message_type: 'tool_return_message', tool_call_id: 'call', id: 'synthetic-tool-return-stream-call' }
    expect(isLettaToolProgress(progress)).toBe(true)
    for (const id of ['', 'synthetic-tool-return-final', 'synthetic-interrupt-tool-return-final', 'synthetic-tool-return-stream-other', 'synthetic-tool-return-stream-call-extra'])
      expect(isLettaToolProgress({ ...progress, id })).toBe(false)
    expect(isLettaToolProgress({ ...progress, tool_call_id: '' })).toBe(false)
    expect(isLettaToolProgress({ ...progress, message_type: 'client_tool_end' })).toBe(false)
    expect(isLettaToolProgress(null)).toBe(false)
  })
})

describe('lettaToolPayload', () => {
  it('preserves the stored payload and normalizes only a present wrapper', () => {
    const payload = { message_type: 'tool_return_message', tool_call_id: 'call' }
    expect(lettaToolPayload(payload)).toBe(payload)
    expect(lettaToolPayload({ payload })).toBe(payload)
    const emptyWrapper = { payload: {} }
    expect(lettaToolPayload(emptyWrapper)).toBe(emptyWrapper)
    for (const value of [null, undefined, false, 0, [], 'native'])
      expect(lettaToolPayload(value)).toBeNull()
  })
})

describe('lettaReturnedData', () => {
  it.each([
    { label: 'empty', value: '' },
    { label: 'zero', value: 0 },
    { label: 'false', value: false },
    { label: 'null', value: null },
    { label: 'negative', value: -7 },
    { label: 'unicode', value: '한글😀' },
    { label: 'large', value: 'complete'.repeat(20000) },
  ])('preserves present $label native output', ({ value }) => {
    const source = { tool_call_id: 'call', status: 'success', tool_return: value }
    expect(lettaReturnedData(source)).toEqual({ kind: 'present', value, status: 'success' })
    expect(lettaReturnedData({
      tool_call_id: 'call',
      status: 'success',
      tool_returns: [{ tool_call_id: 'foreign', tool_return: 'other' }, { tool_call_id: 'call', tool_return: value }],
    })).toEqual({ kind: 'present', value, status: 'success' })
  })

  it('distinguishes absent returned data from null returned data', () => {
    expect(lettaReturnedData(null)).toEqual({ kind: 'absent' })
    expect(lettaReturnedData({ tool_call_id: 'call' })).toEqual({ kind: 'absent' })
    expect(lettaReturnedData({ tool_call_id: 'call', tool_returns: [{ tool_call_id: 'call' }] })).toEqual({ kind: 'absent' })
    expect(lettaReturnedData({ tool_call_id: 'call', tool_return: null })).toEqual({ kind: 'present', value: null, status: undefined })
  })

  it('refuses foreign, duplicate, malformed, and contradictory composite returns', () => {
    const source = { tool_call_id: 'call', status: 'success', tool_return: 'actual' }
    for (const tool_returns of [null, false, {}, [], [{ tool_call_id: 'other', tool_return: 'actual' }], [{ tool_call_id: 'call', tool_return: 'actual' }, { tool_call_id: 'call', tool_return: 'actual' }], [{ tool_call_id: 'call', tool_return: 'different' }], [{ tool_call_id: 'call', status: 'error', tool_return: 'actual' }]])
      expect(lettaReturnedData({ ...source, tool_returns })).toEqual({ kind: 'invalid' })
    expect(lettaReturnedData({ tool_return: 'actual', tool_returns: [{ tool_call_id: 'call', tool_return: 'actual' }] })).toEqual({ kind: 'invalid' })
  })

  it('compares structured returned data without changing its native object', () => {
    const value = { zero: 0, false: false, items: ['한글', null] }
    const source = { tool_call_id: 'call', tool_return: value, tool_returns: [{ tool_call_id: 'call', tool_return: { items: ['한글', null], false: false, zero: 0 } }] }
    const result = lettaReturnedData(source)
    expect(result.kind).toBe('present')
    if (result.kind !== 'present')
      throw new Error('The matching native structured return must remain present.')
    expect(result.value).toBe(value)
  })

  it('compares deep valid native JSON without exhausting the stack', () => {
    const depth = 12000
    const first = `${'{"next":'.repeat(depth)}{"zero":0,"false":false}${'}'.repeat(depth)}`
    const same = `${'{"next":'.repeat(depth)}{"false":false,"zero":0}${'}'.repeat(depth)}`
    const source: unknown = JSON.parse(`{"tool_call_id":"call","tool_return":${first},"tool_returns":[{"tool_call_id":"call","tool_return":${same}}]}`)
    if (!isObject(source))
      throw new Error('The native deep JSON fixture must contain an object.')
    expect(() => lettaReturnedData(source)).not.toThrow()
    expect(lettaReturnedData(source).kind).toBe('present')
    const different = `${'{"next":'.repeat(depth)}{"false":false,"zero":1}${'}'.repeat(depth)}`
    const conflict: unknown = JSON.parse(`{"tool_call_id":"call","tool_return":${first},"tool_returns":[{"tool_call_id":"call","tool_return":${different}}]}`)
    if (!isObject(conflict))
      throw new Error('The native deep JSON conflict must contain an object.')
    expect(lettaReturnedData(conflict).kind).toBe('invalid')
  })
})
