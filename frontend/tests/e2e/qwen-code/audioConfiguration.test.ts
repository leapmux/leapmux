import { describe, expect, it } from 'vitest'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { QWEN_AUTH_TYPE } from '../helpers/qwenEnvironment'
import { qwenAudioConfiguration } from './audioConfiguration'

describe('qwenAudioConfiguration', () => {
  const model = {
    id: MOCK_MODELS.qwen,
    generationConfig: {
      modalities: { image: true, pdf: true },
    },
  }
  it('keeps endpoints unchanged and declares each model modality without mutating the input', () => {
    const value = {
      modelProviders: {
        [QWEN_AUTH_TYPE]: [
          model,
          { ...model, id: 'alternate' },
        ],
      },
      security: {
        auth: { selectedType: QWEN_AUTH_TYPE },
      },
    }
    const before = structuredClone(value)
    expect(qwenAudioConfiguration(value)).toEqual({
      ...value,
      modelProviders: {
        [QWEN_AUTH_TYPE]: [
          {
            ...model,
            generationConfig: {
              modalities: { image: true, pdf: true, audio: true },
            },
          },
          {
            ...model,
            id: 'alternate',
            generationConfig: {
              modalities: { image: true, pdf: true, audio: false },
            },
          },
        ],
      },
    })
    expect(value).toEqual(before)
  })
  it.each([
    null,
    {},
    { modelProviders: {} },
    {
      modelProviders: { [QWEN_AUTH_TYPE]: [] },
    },
    {
      modelProviders: {
        [QWEN_AUTH_TYPE]: [model, model],
      },
    },
    {
      modelProviders: {
        [QWEN_AUTH_TYPE]: [
          { id: MOCK_MODELS.qwen },
        ],
      },
    },
  ])('refuses invalid private models: %j', (value) => {
    expect(() => qwenAudioConfiguration(value)).toThrow('Qwen audio fixture')
  })
})
