import { describe, expect, it } from 'vitest'
import { parseDroidNativeSettingsSnapshot } from './settingsSnapshot'

describe('parseDroidNativeSettingsSnapshot', () => {
  it('reads effective settings from the native session file', () => {
    expect(parseDroidNativeSettingsSnapshot(JSON.stringify({
      model: 'claude-fable-5.1',
      reasoningEffort: 'high',
      interactionMode: 'spec',
      autonomyLevel: 'off',
      tokenUsage: { inputTokens: 42 },
    }))).toEqual({
      modelId: 'claude-fable-5.1',
      reasoningEffort: 'high',
      interactionMode: 'spec',
      autonomyLevel: 'off',
    })
  })

  it('keeps absent fields absent', () => {
    expect(parseDroidNativeSettingsSnapshot('{"model":"custom:Droid-0"}')).toEqual({
      modelId: 'custom:Droid-0',
      reasoningEffort: undefined,
      interactionMode: undefined,
      autonomyLevel: undefined,
    })
  })

  it.each(['null', '[]', '7', '""'])('refuses a non-object settings file: %s', (raw) => {
    expect(() => parseDroidNativeSettingsSnapshot(raw)).toThrow('must contain an object')
  })

  it('preserves a malformed file error', () => {
    expect(() => parseDroidNativeSettingsSnapshot('{bad')).toThrow(SyntaxError)
  })

  it('ignores malformed values without inventing settings', () => {
    expect(parseDroidNativeSettingsSnapshot('{"model":null,"reasoningEffort":0,"interactionMode":{},"autonomyLevel":[]}')).toEqual({
      modelId: undefined,
      reasoningEffort: undefined,
      interactionMode: undefined,
      autonomyLevel: undefined,
    })
  })

  it('keeps an explicit empty value distinct from an absent field', () => {
    expect(parseDroidNativeSettingsSnapshot('{"model":""}').modelId).toBe('')
    expect(parseDroidNativeSettingsSnapshot('{}').modelId).toBeUndefined()
  })
})
