import { isObject } from '../../../src/lib/jsonPick'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { QWEN_AUTH_TYPE } from '../helpers/qwenEnvironment'

/** Enable only the existing primary model's native audio modality in the private fixture. */
export function qwenAudioConfiguration(value: unknown): Record<string, unknown> {
  const providers = isObject(value) && isObject(value.modelProviders) ? value.modelProviders : undefined
  const models = providers?.[QWEN_AUTH_TYPE]
  if (!isObject(value) || !Array.isArray(models) || !models.every(isObject))
    throw new Error('The Qwen audio fixture requires its private model providers.')
  const primary = models.filter(model => model.id === MOCK_MODELS.qwen)
  if (primary.length !== 1 || !isObject(primary[0]?.generationConfig) || !isObject(primary[0].generationConfig.modalities))
    throw new Error('The Qwen audio fixture requires one primary mock model with native modalities.')
  const configured = structuredClone(value)
  const configuredModels = (configured.modelProviders as Record<string, Record<string, unknown>[]>)[QWEN_AUTH_TYPE]!
  for (const model of configuredModels) {
    if (!isObject(model.generationConfig) || !isObject(model.generationConfig.modalities))
      throw new Error('The Qwen audio fixture requires native modalities for every mock model.')
    const generation = model.generationConfig
    model.generationConfig = { ...generation, modalities: { ...generation.modalities as Record<string, unknown>, audio: model.id === MOCK_MODELS.qwen } }
  }
  return configured
}
