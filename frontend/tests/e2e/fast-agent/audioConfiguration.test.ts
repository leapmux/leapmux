import { describe, expect, it } from 'vitest'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { fastagentAudioConfiguration } from './audioConfiguration'

describe('fastagentAudioConfiguration', () => {
  const original = 'default_model: "gpt-4o"\nopenai:\n  base_url: "http://127.0.0.1:4321/v1"\n'
  it('retains the original configuration and appends only the mock Google route', () => {
    expect(fastagentAudioConfiguration(original, 'http://127.0.0.1:4321/v1')).toBe(`${original}\ngoogle:\n  api_key: "${MODEL_KEY}"\n  base_url: "http://127.0.0.1:4321"\n`)
  })
  it.each(['', '   ', `${original}google:\n  api_key: existing\n`])('refuses an absent configuration or a duplicate Google provider: %j', (content) => {
    expect(() => fastagentAudioConfiguration(content, 'http://127.0.0.1:4321/v1')).toThrow('existing configuration')
  })
  it.each(['https://127.0.0.1:4321/v1', 'http://example.com/v1', 'http://user:secret@localhost:4321/v1', 'http://localhost:4321/v1?key=value', 'http://localhost:4321/v1#fragment', 'http://localhost:4321/another', 'invalid'])('refuses an unsafe or unexpected native endpoint: %j', (endpoint) => {
    expect(() => fastagentAudioConfiguration(original, endpoint)).toThrow('Fast Agent audio fixture')
  })
})
