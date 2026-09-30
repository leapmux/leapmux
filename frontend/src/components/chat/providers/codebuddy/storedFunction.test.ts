import { describe, expect, it } from 'vitest'
import { storedFunctionArgs, storedFunctionCallID, storedFunctionFailed, storedFunctionIsProgress, storedFunctionOutputText } from './storedFunction'

describe('storedFunctionCallID', () => {
  it('uses the native call ID instead of the distinct record ID', () => {
    expect(storedFunctionCallID({ id: 'record-1', callId: 'call-1' })).toBe('call-1')
    expect(storedFunctionCallID({ id: 'record-2', call_id: 'call-1' })).toBe('call-1')
    expect(storedFunctionCallID({ id: 'record-only' })).toBeUndefined()
  })

  it('rejects conflicting call ID fields', () => {
    expect(storedFunctionCallID({ callId: 'call-1', call_id: 'call-2' })).toBeUndefined()
    expect(storedFunctionCallID({ callId: 'call-1', call_id: 'call-1' })).toBe('call-1')
  })
})

describe('storedFunctionArgs', () => {
  it('reads object arguments and keeps malformed text visible', () => {
    expect(storedFunctionArgs('{"file_path":"a.txt"}')).toEqual({ file_path: 'a.txt' })
    expect(storedFunctionArgs({ file_path: 'b.txt' })).toEqual({ file_path: 'b.txt' })
    expect(storedFunctionArgs('{bad')).toEqual({ arguments: '{bad' })
    expect(storedFunctionArgs(null)).toEqual({})
  })
})

describe('storedFunctionOutputText', () => {
  it('keeps a native text object and each array block in order', () => {
    expect(storedFunctionOutputText({ type: 'text', text: 'result' })).toBe('result')
    expect(storedFunctionOutputText([{ type: 'text', text: 'first' }, { type: 'text', text: 'second' }])).toBe('first\nsecond')
    expect(storedFunctionOutputText({ code: 7 })).toBe('{"code":7}')
    expect(storedFunctionOutputText(undefined)).toBe('')
  })
})

describe('storedFunctionIsProgress', () => {
  it('separates streaming progress from a final result and explicit failure', () => {
    expect(storedFunctionIsProgress({ status: 'in_progress' })).toBe(true)
    expect(storedFunctionIsProgress({ status: 'completed' })).toBe(false)
    expect(storedFunctionFailed({ status: 'failed' })).toBe(true)
    expect(storedFunctionFailed({ status: 'completed', is_error: true })).toBe(true)
    expect(storedFunctionFailed({ status: 'completed' })).toBe(false)
  })
})
