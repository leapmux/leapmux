import { describe, expect, it } from 'vitest'
import { codexAudioModelCatalog } from './audioCatalog'

describe('codexAudioModelCatalog', () => {
  it('supplies the native audio modality on the requested private model', () => {
    const catalog = codexAudioModelCatalog('private-audio-model')
    expect(catalog.models).toHaveLength(1)
    expect(catalog.models[0]?.slug).toBe('private-audio-model')
    expect(catalog.models[0]?.input_modalities).toEqual(['text', 'image', 'audio'])
    expect(catalog.models[0]?.base_instructions).not.toBe('')
    expect(JSON.parse(JSON.stringify(catalog))).toEqual(catalog)
  })

  it.each(['', ' ', '\n'])('refuses an absent model ID: %j', (model) => {
    expect(() => codexAudioModelCatalog(model)).toThrow('model ID')
  })
})
