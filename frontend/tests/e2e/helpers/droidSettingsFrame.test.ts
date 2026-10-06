import { describe, expect, it } from 'vitest'
import { parseDroidNativeSettingsUpdates } from './droidSettingsFrame'

describe('parseDroidNativeSettingsUpdates', () => {
  it('reads the native request ID and effective modern settings', () => {
    expect(parseDroidNativeSettingsUpdates(JSON.stringify({
      type: 'settings_updated',
      requestId: 'leapmux-2',
      settings: { modelId: 'claude-fable-5.1', reasoningEffort: 'high', interactionMode: 'spec', autonomyLevel: 'off' },
    }))).toEqual([{
      requestId: 'leapmux-2',
      modelId: 'claude-fable-5.1',
      reasoningEffort: 'high',
      interactionMode: 'spec',
      autonomyLevel: 'off',
    }])
  })

  it('keeps absent fields absent and ignores malformed or unrelated rows', () => {
    expect(parseDroidNativeSettingsUpdates('{"type":"settings_updated","settings":{"modelId":"custom:Droid-0"}}')).toEqual([{
      requestId: undefined,
      modelId: 'custom:Droid-0',
      reasoningEffort: undefined,
      interactionMode: undefined,
      autonomyLevel: undefined,
    }])
    expect(parseDroidNativeSettingsUpdates('{bad')).toEqual([])
    expect(parseDroidNativeSettingsUpdates('{"type":"tool_result"}')).toEqual([])
    expect(parseDroidNativeSettingsUpdates('{"type":"settings_updated","settings":null}')).toEqual([])
  })

  it('reads a native update inside a persisted notification thread', () => {
    expect(parseDroidNativeSettingsUpdates(JSON.stringify({
      type: 'notification_thread',
      messages: [
        { type: 'droid_working_state_changed', newState: 'idle' },
        {
          type: 'settings_updated',
          requestId: 'leapmux-7',
          settings: { modelId: 'custom:Droid-1', reasoningEffort: 'none', interactionMode: 'auto', autonomyLevel: 'high' },
        },
      ],
    }))).toEqual([{
      requestId: 'leapmux-7',
      modelId: 'custom:Droid-1',
      reasoningEffort: 'none',
      interactionMode: 'auto',
      autonomyLevel: 'high',
    }])
  })

  it('reads every update of one notification thread in its stored order', () => {
    const update = (requestId: string, modelId: string) => ({ type: 'settings_updated', requestId, settings: { modelId } })
    const updates = parseDroidNativeSettingsUpdates(JSON.stringify({
      type: 'notification_thread',
      messages: [update('leapmux-1', 'custom:Droid-0'), { type: 'tool_result' }, update('leapmux-2', 'custom:Droid-1')],
    }))
    expect(updates.map(entry => [entry.requestId, entry.modelId])).toEqual([['leapmux-1', 'custom:Droid-0'], ['leapmux-2', 'custom:Droid-1']])
  })

  it('reads no update from a notification thread without a message list', () => {
    expect(parseDroidNativeSettingsUpdates('{"type":"notification_thread","messages":null}')).toEqual([])
  })
})
