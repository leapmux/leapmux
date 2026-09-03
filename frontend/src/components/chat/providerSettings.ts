import type { AvailableOptionGroup } from '~/generated/proto/leapmux/v1/agent_pb'

/** One atomic change to one or more provider settings. */
export interface ProviderSettingChange {
  sets: Record<string, string>
}

export type ProviderSettingChangeHandler = (change: ProviderSettingChange) => void | Promise<void>

/** One provider-native permission preset. */
export interface ProviderPermissionPreset extends ProviderSettingChange {
  sets: Record<string, string>
}

/** The standard permission presets that a provider can offer. */
export interface ProviderPermissionPresets {
  smart?: ProviderPermissionPreset
  bypass?: ProviderPermissionPreset
}

/** A usable bypass action. The UI receives it only when both parts exist. */
export interface BypassController {
  settings: ProviderPermissionPreset
  apply: ProviderSettingChangeHandler
}

/** Reports whether the catalog offers every group and value in a preset. */
export function permissionPresetAvailable(
  preset: ProviderPermissionPreset | undefined,
  groups: AvailableOptionGroup[] | undefined,
): preset is ProviderPermissionPreset {
  if (!preset)
    return false
  const entries = Object.entries(preset.sets)
  if (entries.length === 0)
    return false
  return entries.every(([groupId, value]) =>
    groups?.some(group => group.id === groupId && group.mutable && group.options.some(option => option.id === value)) ?? false,
  )
}
