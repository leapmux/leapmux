import { describe, expect, it } from 'vitest'
import { nativeUserStrings } from './attachmentModelProbe'

describe('nativeUserStrings', () => {
  it('reads Google user bytes without model media, tool results, or instructions', () => {
    const body = {
      systemInstruction: { parts: [{ text: 'SYSTEM_ONLY' }] },
      tools: [{ functionDeclarations: [{ description: 'SCHEMA_ONLY' }] }],
      contents: [
        { role: 'model', parts: [{ inlineData: { mimeType: 'image/png', data: 'MODEL_ONLY' } }] },
        { role: 'user', parts: [{ text: 'ACTUAL_USER' }, { inlineData: { mimeType: 'audio/wav', data: 'ACTUAL_BYTES' } }] },
        { role: 'user', parts: [{ functionResponse: { response: { data: 'RESULT_ONLY' } } }] },
      ],
    }
    expect(nativeUserStrings(body)).toEqual(['ACTUAL_USER', 'audio/wav', 'ACTUAL_BYTES'])
  })

  it.each([undefined, null, false, 0, '', {}, { contents: null }, { contents: [null, {}, { role: 'user', parts: null }] }])('handles absent or malformed native user contents: %j', (body) => {
    expect(nativeUserStrings(body)).toEqual([])
  })

  it('preserves the existing generic API user content reader', () => {
    expect(nativeUserStrings({ messages: [{ role: 'system', content: 'SYSTEM_ONLY' }, { role: 'user', content: [{ type: 'text', text: 'ACTUAL_USER' }, { type: 'image_url', image_url: { url: 'ACTUAL_IMAGE' } }] }] }))
      .toEqual(['text', 'ACTUAL_USER', 'image_url', 'ACTUAL_IMAGE'])
  })
})
