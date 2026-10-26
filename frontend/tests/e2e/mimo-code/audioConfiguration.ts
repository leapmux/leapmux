import { isObject } from '../../../src/lib/jsonPick'
import { MOCK_MODELS, MOCK_PROVIDER_IDS } from '../helpers/mockAgentEnvironment'

export const MIMO_AUDIO_MODEL_ALIAS = 'leapmux-audio-fixture'
export const MIMO_AUDIO_MODEL_ID = `${MOCK_PROVIDER_IDS.openCode}/${MIMO_AUDIO_MODEL_ALIAS}`

/** Add an audio-capable native alias without changing the private provider or its wire model. */
export function mimoAudioConfiguration(value: unknown): Record<string, unknown> {
  const provider = isObject(value) && isObject(value.provider) ? value.provider[MOCK_PROVIDER_IDS.openCode] : undefined
  const models = isObject(provider) && isObject(provider.models) ? provider.models : undefined
  const model = models?.[MOCK_MODELS.zai]
  if (!isObject(model) || !isObject(model.modalities) || !Array.isArray(model.modalities.input) || !model.modalities.input.every(part => typeof part === 'string'))
    throw new Error('The MiMo audio fixture requires its private mock model and input modalities.')
  if (model.id !== undefined && model.id !== MOCK_MODELS.zai)
    throw new Error('The MiMo audio fixture must retain the private mock wire model.')
  if (models?.[MIMO_AUDIO_MODEL_ALIAS] !== undefined)
    throw new Error('The private MiMo audio model alias already exists.')
  return {
    provider: {
      [MOCK_PROVIDER_IDS.openCode]: {
        models: {
          [MIMO_AUDIO_MODEL_ALIAS]: {
            ...structuredClone(model),
            id: MOCK_MODELS.zai,
            name: 'LeapMux audio fixture',
            attachment: true,
            modalities: { ...structuredClone(model.modalities), input: [...new Set([...model.modalities.input, 'audio'])] },
          },
        },
      },
    },
  }
}
