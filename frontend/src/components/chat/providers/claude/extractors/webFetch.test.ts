import { describe, expect, it } from 'vitest'
import { claudeWebFetchFromToolResult } from './webFetch'

describe('claudeWebFetchFromToolResult', () => {
  it('returns null when tool_use_result is missing', () => {
    expect(claudeWebFetchFromToolResult(null, 'fallback')).toBeNull()
    expect(claudeWebFetchFromToolResult(undefined, 'fallback')).toBeNull()
  })

  it('returns null when code is not a number', () => {
    expect(claudeWebFetchFromToolResult({ code: 'oops' }, '')).toBeNull()
    expect(claudeWebFetchFromToolResult({}, '')).toBeNull()
  })

  it('extracts the full structured payload', () => {
    expect(claudeWebFetchFromToolResult({
      code: 200,
      codeText: 'OK',
      bytes: 4096,
      durationMs: 850,
      result: '# Body',
      url: 'https://example.com/page',
    }, 'fallback')).toEqual({
      code: 200,
      codeText: 'OK',
      bytes: 4096,
      durationMs: 850,
      result: '# Body',
      url: 'https://example.com/page',
    })
  })

  it('falls back to resultContent for the markdown body when result is missing', () => {
    expect(claudeWebFetchFromToolResult({ code: 404 }, 'fallback body')).toEqual({
      code: 404,
      codeText: '',
      bytes: 0,
      durationMs: 0,
      result: 'fallback body',
      url: undefined,
    })
  })
})
