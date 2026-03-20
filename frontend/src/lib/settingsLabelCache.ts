import type { AvailableModel, AvailableOptionGroup } from '~/generated/leapmux/v1/agent_pb'

/**
 * Global cache of model/effort/option display names, populated from AgentInfo.
 * Used by notification renderers to show human-readable labels for settings changes
 * when the notification payload doesn't include inline labels (e.g., older messages).
 */
const modelLabels = new Map<string, string>()
const effortLabels = new Map<string, string>()
const optionGroupLabels = new Map<string, Map<string, string>>()

/** Update the cache from an availableModels list (called when AgentInfo arrives). */
export function updateSettingsLabelCache(models: AvailableModel[], optionGroups?: AvailableOptionGroup[]): void {
  for (const m of models) {
    if (m.displayName)
      modelLabels.set(m.id, m.displayName)
    for (const e of m.supportedEfforts) {
      if (e.name)
        effortLabels.set(e.id, e.name)
    }
  }
  if (optionGroups) {
    for (const group of optionGroups) {
      let groupMap = optionGroupLabels.get(group.key)
      if (!groupMap) {
        groupMap = new Map()
        optionGroupLabels.set(group.key, groupMap)
      }
      for (const opt of group.options) {
        if (opt.name)
          groupMap.set(opt.id, opt.name)
      }
    }
  }
}

/** Look up a cached display name for a model, effort, or option group ID. */
export function getCachedSettingsLabel(key: string, id: string): string | undefined {
  if (key === 'model')
    return modelLabels.get(id)
  if (key === 'effort')
    return effortLabels.get(id)
  return optionGroupLabels.get(key)?.get(id)
}
