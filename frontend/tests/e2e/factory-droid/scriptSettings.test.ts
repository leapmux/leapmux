import { describe, expect, it } from 'vitest'
import { droidScriptConfiguration } from './scriptSettings'

const models = [{ id: 'custom:Droid-0', model: 'isolated-model', baseUrl: 'http://127.0.0.1:1234', apiKey: 'mock-only' }]

describe('droidScriptConfiguration', () => {
  it('selects native script mode and permits the actual custom models without changing their configuration', () => {
    const defaults = { model: 'custom:Droid-0', reasoningEffort: 'none', autonomyMode: 'normal' }
    const configuration = droidScriptConfiguration({ customModels: models, sessionDefaultSettings: defaults })
    expect(configuration.settings.toolExecutionMode).toBe('script_only')
    expect(Object.hasOwn(configuration.settings, 'general')).toBe(false)
    expect(configuration.settings.customModels).toEqual(models)
    expect(configuration.settings.sessionDefaultSettings).toEqual(defaults)
    expect(configuration.snapshot).toEqual({ flags: {}, configs: { script_tool_models: { models: ['custom:Droid-0'] } } })
  })

  it('preserves zero and empty native values without mutating the input', () => {
    const settings = Object.freeze({ customModels: models, toolExecutionMode: 'direct_only', zero: 0, disabled: false, empty: '', nullable: null })
    const configuration = droidScriptConfiguration(settings)
    expect(configuration.settings).toEqual({ ...settings, toolExecutionMode: 'script_only' })
    expect(settings.toolExecutionMode).toBe('direct_only')
    expect(configuration.settings).not.toBe(settings)
  })

  it('keeps an unrelated general field without putting the execution mode inside it', () => {
    const general = { unrelated: 'unchanged' }
    const configuration = droidScriptConfiguration({ customModels: models, general })
    expect(configuration.settings.general).toEqual(general)
    expect(Object.hasOwn(general, 'toolExecutionMode')).toBe(false)
    expect(configuration.settings.toolExecutionMode).toBe('script_only')
  })

  it('permits each configured custom model ID once', () => {
    const customModels = [...models, { ...models[0], id: 'custom:Alternate-1' }]
    expect(droidScriptConfiguration({ customModels }).snapshot.configs).toEqual({
      script_tool_models: { models: ['custom:Droid-0', 'custom:Alternate-1'] },
    })
  })

  it.each([{}, { customModels: [] }, { customModels: null }, { customModels: [null] }, { customModels: [{ id: '' }] }, { customModels: [{ id: ' ' }] }, { customModels: [{ id: 0 }] }, { customModels: [...models, ...models] }])('rejects settings without distinct nonempty custom model IDs: %j', (settings) => {
    expect(() => droidScriptConfiguration(settings)).toThrow('distinct nonempty custom model IDs')
  })

  it.each([undefined, null, [], '', 0, false])('rejects a malformed settings record: %j', (settings) => {
    expect(() => droidScriptConfiguration(settings)).toThrow('settings object')
  })
})
