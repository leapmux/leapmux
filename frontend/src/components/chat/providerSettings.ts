import type { AvailableOptionGroup } from '~/generated/proto/leapmux/v1/agent_pb'
import { optionGroup, valueValidForGroup } from './settingsGroups'

/** One atomic change to one or more provider settings. */
export interface ProviderSettingChange {
  sets: Record<string, string>
}

export type ProviderSettingChangeHandler = (change: ProviderSettingChange) => void | Promise<void>

/**
 * One provider-native permission preset: the complete settings change that selects it.
 *
 * A preset names whatever axes ITS provider needs, and nothing more — Claude switches
 * one permission mode, Codex switches network, sandbox and approval together, and
 * Copilot switches an axis that is not the permission mode at all. So no key is
 * guaranteed, and a consumer that needs a specific one must check for it (see
 * `./controls/permissionPresets`, which reads a preset's permission mode only when
 * it carries one).
 */
export type ProviderPermissionPreset = ProviderSettingChange

/** The standard permission presets that a provider can offer. */
export interface ProviderPermissionPresets {
  smart?: ProviderPermissionPreset
  bypass?: ProviderPermissionPreset
}

/**
 * The usable permission presets a control request can apply: every preset the live
 * catalog offers, plus the ONE handler that applies a settings change — the composer's
 * `onSettingChange`, the same call the `[+]` menu's permission items make. A preset
 * the catalog does not currently offer is `undefined`, so the pill for it is not drawn.
 */
export interface PermissionPresetController {
  smart?: ProviderPermissionPreset
  bypass?: ProviderPermissionPreset
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
    !!optionGroup(groups, groupId)?.mutable && valueValidForGroup(groups, groupId, value),
  )
}
