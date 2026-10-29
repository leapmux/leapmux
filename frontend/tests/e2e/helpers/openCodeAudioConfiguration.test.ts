import { describe, expect, it } from 'vitest'
import { GEMINI_MODEL_ID, MOCK_PROVIDER_IDS } from './mockAgentEnvironment'
import { OPENCODE_AUDIO_MODEL_ALIAS, OPENCODE_AUDIO_MODEL_ID, openCodeAudioConfiguration } from './openCodeAudioConfiguration'

describe('openCodeAudioConfiguration', () => {
  function configuration(input: unknown = ['text', 'image', 'pdf'], extra: Record<string, unknown> = {}, endpoint = 'http://127.0.0.1:4321/v1') {
    return {
      model: `${MOCK_PROVIDER_IDS.openCode}/original`,
      enabled_providers: [MOCK_PROVIDER_IDS.openCode],
      provider: {
        [MOCK_PROVIDER_IDS.openCode]: {
          options: { apiKey: 'private-credential', baseURL: endpoint },
          models: {
            original: {
              id: 'original',
              modalities: {
                input,
                output: ['text'],
              },
              variants: {
                high: { reasoningEffort: 'high' },
              },
              ...extra,
            },
          },
        },
      },
    }
  }
  it('adds only the Google alias and retains the original provider configuration', () => {
    const original = configuration()
    const before = structuredClone(original)
    expect(OPENCODE_AUDIO_MODEL_ID).toBe(`${MOCK_PROVIDER_IDS.openCode}/${OPENCODE_AUDIO_MODEL_ALIAS}`)
    expect(openCodeAudioConfiguration(original)).toEqual({
      textModelID: original.model,
      configuration: {
        provider: {
          [MOCK_PROVIDER_IDS.openCode]: {
            models: {
              [OPENCODE_AUDIO_MODEL_ALIAS]: {
                id: GEMINI_MODEL_ID,
                name: 'LeapMux audio fixture',
                attachment: true,
                provider: { npm: '@ai-sdk/google', api: 'http://127.0.0.1:4321/v1beta' },
                modalities: {
                  input: ['text', 'image', 'pdf', 'audio'],
                  output: ['text'],
                },
                variants: {
                  high: { reasoningEffort: 'high' },
                },
              },
            },
          },
        },
      },
    })
    expect(original).toEqual(before)
  })
  it('does not duplicate an existing audio modality', () => {
    expect(openCodeAudioConfiguration(configuration(['text', 'audio'])).configuration).toMatchObject({
      provider: {
        [MOCK_PROVIDER_IDS.openCode]: {
          models: {
            [OPENCODE_AUDIO_MODEL_ALIAS]: {
              modalities: {
                input: ['text', 'audio'],
              },
            },
          },
        },
      },
    })
  })
  it.each([
    null,
    {},
    { model: 'external/model' },
    { model: `${MOCK_PROVIDER_IDS.openCode}/missing`, provider: {} },
  ])('refuses an absent private model: %j', (value) => {
    expect(() => openCodeAudioConfiguration(value)).toThrow('OpenCode audio fixture')
  })
  it.each([
    null,
    'text',
    ['text', 42],
  ])('refuses invalid native input modalities: %j', (input) => {
    expect(() => openCodeAudioConfiguration(configuration(input))).toThrow('input modalities')
  })
  it('refuses a different wire model or an existing alias', () => {
    expect(() => openCodeAudioConfiguration(configuration(['text'], { id: 'another' }))).toThrow('wire model')
    const original = configuration()
    const provider = original.provider[MOCK_PROVIDER_IDS.openCode]
    expect(() => openCodeAudioConfiguration({
      ...original,
      provider: {
        [MOCK_PROVIDER_IDS.openCode]: {
          ...provider,
          models: { ...provider.models, [OPENCODE_AUDIO_MODEL_ALIAS]: {} },
        },
      },
    })).toThrow('alias already exists')
  })
  it.each([
    'https://localhost:4321/v1',
    'http://example.com/v1',
    'http://user:secret@localhost:4321/v1',
    'http://localhost:4321/v1?key=value',
    'http://localhost:4321/another',
  ])('refuses an unsafe or unexpected endpoint: %j', (endpoint) => {
    expect(() => openCodeAudioConfiguration(configuration(['text'], {}, endpoint))).toThrow('OpenCode audio fixture')
  })
})
