import { describe, expect, it } from 'vitest'
import { googleFunctionDeclarations, googleLastUserText, googlePartsText } from './googleModelContent'

describe('googlePartsText', () => {
  it('keeps native text order and excludes nested tool results and media', () => {
    expect(googlePartsText([{ text: 'First 🧪' }, { functionResponse: { response: { text: 'RESULT_ONLY' } } }, { inlineData: { data: 'MEDIA_ONLY' } }, { text: '' }, { text: 'Last' }])).toBe('First 🧪\n\nLast')
  })

  it.each([undefined, null, false, 0, '', {}, [null, false, 0, {}, { text: 0 }]])('returns no text for malformed or absent parts: %j', (parts) => {
    expect(googlePartsText(parts)).toBe('')
  })
})

describe('googleLastUserText', () => {
  it('preserves a preceding user prompt after result-only and model rows', () => {
    expect(googleLastUserText([
      { role: 'user', parts: [{ text: 'First' }] },
      { role: 'user', parts: [{ text: 'Current' }] },
      { role: 'model', parts: [{ text: 'MODEL_ONLY' }] },
      { role: 'user', parts: [{ functionResponse: { response: { text: 'RESULT_ONLY' } } }] },
    ])).toBe('Current')
  })

  it.each([undefined, null, false, 0, '', [], [{}], [{ role: 'user', parts: [] }], [{ role: 'model', parts: [{ text: 'MODEL_ONLY' }] }]])('returns no user text for absent or non-user contents: %j', (contents) => {
    expect(googleLastUserText(contents)).toBe('')
  })
})

describe('googleFunctionDeclarations', () => {
  it('preserves original declaration objects and native duplicates', () => {
    const declaration = { name: 'read_file', parametersJsonSchema: { type: 'object' } }
    const declarations = googleFunctionDeclarations([{ functionDeclarations: [declaration] }, { functionDeclarations: [declaration] }])
    expect(declarations).toHaveLength(2)
    expect(declarations[0]).toBe(declaration)
    expect(declarations[1]).toBe(declaration)
    expect(googleFunctionDeclarations([])).toEqual([])
  })

  it.each([undefined, null, false, 0, '', {}, [null], [{}], [{ functionDeclarations: null }], [{ functionDeclarations: [null] }], [{ functionDeclarations: [{ name: '' }] }], [{ functionDeclarations: [{ name: false }] }]])('rejects malformed native catalog input: %j', (tools) => {
    expect(() => googleFunctionDeclarations(tools)).toThrow(/catalog|declaration/)
  })
})
