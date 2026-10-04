import { describe, expect, it } from 'vitest'
import { parseDroidNativeSettingsUpdate } from './droidSettingsFrame'

describe('parseDroidNativeSettingsUpdate', () => {
  it('reads the native request ID and effective modern settings', () => {
    expect(parseDroidNativeSettingsUpdate(JSON.stringify({
      type: 'settings_updated',
      requestId: 'leapmux-2',
      settings: { modelId: 'claude-fable-5.1', reasoningEffort: 'high', interactionMode: 'spec', autonomyLevel: 'off' },
    }))).toEqual({
      requestId: 'leapmux-2',
      modelId: 'claude-fable-5.1',
      reasoningEffort: 'high',
      interactionMode: 'spec',
      autonomyLevel: 'off',
    })
  })

  it('keeps absent fields absent and ignores malformed or unrelated rows', () => {
    expect(parseDroidNativeSettingsUpdate('{"type":"settings_updated","settings":{"modelId":"custom:Droid-0"}}')).toEqual({
      requestId: undefined,
      modelId: 'custom:Droid-0',
      reasoningEffort: undefined,
      interactionMode: undefined,
      autonomyLevel: undefined,
    })
    expect(parseDroidNativeSettingsUpdate('{bad')).toBeNull()
    expect(parseDroidNativeSettingsUpdate('{"type":"tool_result"}')).toBeNull()
    expect(parseDroidNativeSettingsUpdate('{"type":"settings_updated","settings":null}')).toBeNull()
  })

  it('reads a native update inside a persisted notification thread', () => {
    expect(parseDroidNativeSettingsUpdate(JSON.stringify({
      type: 'notification_thread',
      messages: [
        { type: 'droid_working_state_changed', newState: 'idle' },
        {
          type: 'settings_updated',
          requestId: 'leapmux-7',
          settings: { modelId: 'custom:Droid-1', reasoningEffort: 'none', interactionMode: 'auto', autonomyLevel: 'high' },
        },
      ],
    }))).toEqual({
      requestId: 'leapmux-7',
      modelId: 'custom:Droid-1',
      reasoningEffort: 'none',
      interactionMode: 'auto',
      autonomyLevel: 'high',
    })
  })
})
