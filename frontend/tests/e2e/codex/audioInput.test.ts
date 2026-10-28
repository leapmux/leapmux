import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { Buffer } from 'node:buffer'
import { describe, expect, it } from 'vitest'
import { codexAudioInputs } from './audioInput'

function request(input: unknown): Pick<MockModelRequestRecord, 'protocol' | 'body'> {
  return { protocol: 'openai-responses', body: { input } }
}

const audio = { type: 'input_audio', audio_url: 'data:audio/wav;base64,AAEC' }

describe('codexAudioInputs', () => {
  it('reads the exact typed payload of the current user turn', () => {
    expect(codexAudioInputs(request([{ role: 'user', content: [audio] }]))).toEqual([{ mimeType: 'audio/wav', bytes: Buffer.from([0, 1, 2]) }])
  })

  it('excludes earlier audio and instruction text', () => {
    expect(codexAudioInputs(request([
      { role: 'user', content: [audio] },
      { role: 'assistant', content: [{ type: 'output_text', text: 'Earlier answer' }] },
      { role: 'user', content: [{ type: 'input_text', text: audio.audio_url }] },
      { role: 'developer', content: [audio] },
    ]))).toEqual([])
  })

  it.each([null, false, 0, -1, '', [], {}].map(value => [value]))('accepts no audio from a missing input array: %j', (input) => {
    expect(codexAudioInputs(request(input))).toEqual([])
  })

  it.each(['', 'data:audio/wav;base64,', 'data:audio/wav;base64,!!!!', 'data:audio/wav;base64,AA', 'https://example.com/audio.wav'])('refuses malformed native audio: %j', (url) => {
    expect(() => codexAudioInputs(request([{ role: 'user', content: [{ ...audio, audio_url: url }] }]))).toThrow('native audio item')
  })

  it('requires the actual model protocol', () => {
    expect(() => codexAudioInputs({ protocol: 'openai-chat-completions', body: {} })).toThrow('Responses API')
  })
})
