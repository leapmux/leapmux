import { describe, expect, it } from 'vitest'
import { droidScriptConfiguration } from './scriptSettings'

describe('droidScriptConfiguration', () => {
  it('writes the native execution mode in the flat settings file and preserves the model configuration', () => {
    const models = [{ id: 'custom:Droid-0', model: 'isolated-model', baseUrl: 'http://127.0.0.1:1234', apiKey: 'mock-only' }]
    const defaults = { model: 'custom:Droid-0', reasoningEffort: 'none', autonomyMode: 'normal' }
    const configuration = droidScriptConfiguration({ customModels: models, sessionDefaultSettings: defaults })
    expect(configuration.settings.toolExecutionMode).toBe('direct_and_script')
    expect(Object.hasOwn(configuration.settings, 'general')).toBe(false)
    expect(configuration.settings.customModels).toEqual(models)
    expect(configuration.settings.sessionDefaultSettings).toEqual(defaults)
    expect(configuration.snapshot).toEqual({ flags: { script_tools: true }, configs: {} })
  })

  it('preserves zero and empty native values without mutating the input', () => {
    const settings = Object.freeze({ toolExecutionMode: 'direct_only', zero: 0, disabled: false, empty: '', nullable: null })
    const configuration = droidScriptConfiguration(settings)
    expect(configuration.settings).toEqual({ toolExecutionMode: 'direct_and_script', zero: 0, disabled: false, empty: '', nullable: null })
    expect(settings.toolExecutionMode).toBe('direct_only')
    expect(configuration.settings).not.toBe(settings)
  })

  it('keeps an unrelated existing general field without placing the execution mode inside it', () => {
    const general = { unrelated: 'unchanged' }
    const configuration = droidScriptConfiguration({ general })
    expect(configuration.settings.general).toEqual(general)
    expect(Object.hasOwn(general, 'toolExecutionMode')).toBe(false)
    expect(configuration.settings.toolExecutionMode).toBe('direct_and_script')
  })

  it('accepts an empty native settings object', () => {
    expect(droidScriptConfiguration({}).settings).toEqual({ toolExecutionMode: 'direct_and_script' })
  })

  it.each([undefined, null, [], '', 0, false])('rejects a malformed settings record %j', (settings) => {
    expect(() => droidScriptConfiguration(settings)).toThrow('settings object')
  })
})
