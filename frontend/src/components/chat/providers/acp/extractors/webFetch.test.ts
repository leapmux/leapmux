import { describe, expect, it } from 'vitest'
import { acpWebFetchFromToolCall } from './webFetch'

describe('acpWebFetchFromToolCall', () => {
  it('returns null for null/undefined toolUse', () => {
    expect(acpWebFetchFromToolCall(null)).toBeNull()
    expect(acpWebFetchFromToolCall(undefined)).toBeNull()
  })

  it('returns null when no rawOutput', () => {
    expect(acpWebFetchFromToolCall({})).toBeNull()
  })

  it('returns null when rawOutput.code is not a number', () => {
    expect(acpWebFetchFromToolCall({ rawOutput: { code: 'oops' } })).toBeNull()
  })

  it('extracts the structured payload when present', () => {
    expect(acpWebFetchFromToolCall({
      rawOutput: {
        code: 200,
        codeText: 'OK',
        bytes: 1024,
        durationMs: 50,
        result: '# Body',
        url: 'https://example.com',
      },
    })).toEqual({
      code: 200,
      codeText: 'OK',
      bytes: 1024,
      durationMs: 50,
      result: '# Body',
      url: 'https://example.com',
    })
  })

  it('defaults missing fields to sensible empty values', () => {
    expect(acpWebFetchFromToolCall({ rawOutput: { code: 500 } })).toEqual({
      code: 500,
      codeText: '',
      bytes: 0,
      durationMs: 0,
      result: '',
      url: undefined,
    })
  })
})
