import type { AvailableModel } from '~/generated/leapmux/v1/agent_pb'

/**
 * Global cache of model/effort display names, populated from AgentInfo.availableModels.
 * Used by notification renderers to show human-readable labels for settings changes
 * when the notification payload doesn't include inline labels (e.g., older messages).
 */
const modelLabels = new Map<string, string>()
const effortLabels = new Map<string, string>()

/** Update the cache from an availableModels list (called when AgentInfo arrives). */
export function updateSettingsLabelCache(models: AvailableModel[]): void {
  for (const m of models) {
    if (m.displayName)
      modelLabels.set(m.id, m.displayName)
    for (const e of m.supportedEfforts) {
      if (e.name)
        effortLabels.set(e.id, e.name)
    }
  }
}

/** Look up a cached display name for a model or effort ID. */
export function getCachedSettingsLabel(key: string, id: string): string | undefined {
  if (key === 'model')
    return modelLabels.get(id)
  if (key === 'effort')
    return effortLabels.get(id)
  return undefined
}
