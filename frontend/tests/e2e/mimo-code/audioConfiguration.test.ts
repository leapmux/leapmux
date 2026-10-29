import { describe, expect, it } from 'vitest'
import { MOCK_MODELS, MOCK_PROVIDER_IDS } from '../helpers/mockAgentEnvironment'
import { MIMO_AUDIO_MODEL_ALIAS, MIMO_AUDIO_MODEL_ID, mimoAudioConfiguration } from './audioConfiguration'

describe('mimoAudioConfiguration', () => {
  function configuration(input: unknown = ['text', 'image', 'pdf'], extra: Record<string, unknown> = {}) {
    return {
      provider: {
        [MOCK_PROVIDER_IDS.openCode]: {
          options: { baseURL: 'http://127.0.0.1:4321/v1' },
          models: {
            [MOCK_MODELS.zai]: {
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

  it('adds an alias with the existing wire model and leaves the original modalities unchanged', () => {
    const original = configuration()
    const before = structuredClone(original)
    const result = mimoAudioConfiguration(original)
    expect(MIMO_AUDIO_MODEL_ID).toBe(`${MOCK_PROVIDER_IDS.openCode}/${MIMO_AUDIO_MODEL_ALIAS}`)
    expect(result).toEqual({
      provider: {
        [MOCK_PROVIDER_IDS.openCode]: {
          models: {
            [MIMO_AUDIO_MODEL_ALIAS]: {
              id: MOCK_MODELS.zai,
              name: 'LeapMux audio fixture',
              attachment: true,
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
    })
    expect(original).toEqual(before)
  })

  it('does not duplicate an existing audio modality', () => {
    const result = mimoAudioConfiguration(configuration(['text', 'audio']))
    expect(result).toMatchObject({
      provider: {
        [MOCK_PROVIDER_IDS.openCode]: {
          models: {
            [MIMO_AUDIO_MODEL_ALIAS]: {
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
    { provider: {} },
    {
      provider: {
        [MOCK_PROVIDER_IDS.openCode]: { models: {} },
      },
    },
  ])('refuses missing native model configuration: %j', (value) => {
    expect(() => mimoAudioConfiguration(value)).toThrow('private mock model')
  })

  it.each([
    null,
    'text',
    ['text', 42],
  ])('refuses invalid native input modalities: %j', (input) => {
    expect(() => mimoAudioConfiguration(configuration(input))).toThrow('input modalities')
  })

  it('refuses a different wire model or a pre-existing alias', () => {
    expect(() => mimoAudioConfiguration(configuration(['text'], { id: 'another-model' }))).toThrow('mock wire model')
    const original = configuration()
    const models = original.provider[MOCK_PROVIDER_IDS.openCode].models
    expect(() => mimoAudioConfiguration({
      provider: {
        [MOCK_PROVIDER_IDS.openCode]: {
          models: { ...models, [MIMO_AUDIO_MODEL_ALIAS]: {} },
        },
      },
    })).toThrow('alias already exists')
  })
})
