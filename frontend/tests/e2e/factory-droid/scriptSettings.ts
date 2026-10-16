import { isObject } from '../../../src/lib/jsonPick'

/** Select Script for the private custom models through Droid's native model configuration. */
export function droidScriptConfiguration(settings: unknown): {
  settings: Record<string, unknown>
  snapshot: { flags: Record<string, never>, configs: { script_tool_models: { models: string[] } } }
} {
  if (!isObject(settings))
    throw new Error('The native Droid Script profile requires a settings object.')
  const models = Array.isArray(settings.customModels)
    ? settings.customModels.map(model => isObject(model) ? model.id : undefined)
    : []
  if (models.length === 0 || models.some(id => typeof id !== 'string' || !id.trim())
    || new Set(models).size !== models.length) {
    throw new Error('The native Droid Script profile requires distinct nonempty custom model IDs.')
  }
  const modelIds = models.filter((id): id is string => typeof id === 'string')
  return {
    settings: { ...settings, toolExecutionMode: 'script_only' },
    snapshot: { flags: {}, configs: { script_tool_models: { models: modelIds } } },
  }
}
