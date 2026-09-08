import type { AvailableOptionGroup } from '~/generated/proto/leapmux/v1/agent_pb'
import { effectiveCurrent, optionGroup, valueValidForGroup } from './settingsGroups'

/** One atomic change to one or more provider settings. */
export interface ProviderSettingChange {
  sets: Record<string, string>
}

export type ProviderSettingChangeHandler = (change: ProviderSettingChange) => void | Promise<void>

/**
 * One provider-native permission preset: the complete settings change that selects it.
 *
 * A preset sets whatever axes ITS provider needs, and nothing more — Claude switches
 * one permission mode, Codex switches network, sandbox and approval together, and
 * Copilot switches an axis that is not the permission mode at all. So no key is
 * guaranteed, and a consumer that needs a specific one must check for it (see
 * `./controls/permissionPresets`, which reads a preset's permission mode only when
 * it carries one).
 */
export type ProviderPermissionPreset = ProviderSettingChange

/**
 * The standard permission presets, from narrower to broader access.
 *
 * The order controls presentation and active-state precedence. A provider can
 * keep two preset axes on together. The broader preset then represents the
 * effective behavior and must win the single selected state.
 *
 * Each label has two forms. A control group already states `Permissions`, so
 * its short label omits that noun. The composer menu states no subject, so its
 * full label includes it.
 */
export const PERMISSION_PRESET_SPECS = [
  { kind: 'smart', shortLabel: 'Smart', fullLabel: 'Smart permissions' },
  { kind: 'bypass', shortLabel: 'Bypass', fullLabel: 'Bypass permissions' },
] as const

export type PermissionPresetKind = typeof PERMISSION_PRESET_SPECS[number]['kind']

/** The standard permission presets that a provider can offer. */
export type ProviderPermissionPresets = Partial<Record<PermissionPresetKind, ProviderPermissionPreset>>

/**
 * The usable permission presets for one control request.
 *
 * `apply` is present when an ordinary request can change settings. A plan
 * approval carries its mode in the response and needs only the preset data.
 */
export type PermissionPresetController = ProviderPermissionPresets & {
  /** Applies a preset outside a plan-approval response. */
  apply?: ProviderSettingChangeHandler
  /**
   * The preset the session already has on, or `undefined` when neither is on.
   *
   * A control request's pill group opens on this, so an Allow keeps the session
   * where the user put it instead of changing it. Only the composer knows it,
   * because it needs the live catalog AND the confirmed values, and a control
   * request's props carry neither.
   */
  active?: PermissionPresetKind
}

/** Returns the non-empty settings entries of a preset. */
function nonEmptyPresetEntries(preset: ProviderPermissionPreset | undefined): [string, string][] | undefined {
  if (!preset)
    return undefined
  const entries = Object.entries(preset.sets)
  return entries.length > 0 ? entries : undefined
}

/**
 * Reports whether the session already has `preset` on: every axis it would set
 * already holds the value it would write.
 *
 * The one implementation of the rule. The composer `[+]` menu disables an item
 * that would change nothing, and a control request's pill group opens on the
 * preset this reports, so a drift between the two would show a menu item as
 * available while the pill says it is already on.
 */
export function permissionPresetActive(
  preset: ProviderPermissionPreset | undefined,
  groups: AvailableOptionGroup[] | undefined,
  values: Record<string, string> | undefined,
): boolean {
  const entries = nonEmptyPresetEntries(preset)
  if (!entries)
    return false
  return entries.every(([groupId, value]) => effectiveCurrent(values, optionGroup(groups, groupId)) === value)
}

/**
 * Which standard preset represents the session's effective permission behavior.
 *
 * The last matching spec wins because the specs run from narrower to broader
 * access. Copilot can keep Assisted Approval and Allow All on together. Allow
 * All supplies the effective behavior, so Bypass wins in that state.
 */
export function activePermissionPreset(
  presets: ProviderPermissionPresets,
  groups: AvailableOptionGroup[] | undefined,
  values: Record<string, string> | undefined,
): PermissionPresetKind | undefined {
  for (let index = PERMISSION_PRESET_SPECS.length - 1; index >= 0; index--) {
    const { kind } = PERMISSION_PRESET_SPECS[index]!
    if (permissionPresetActive(presets[kind], groups, values))
      return kind
  }
  return undefined
}

/** Reports whether the catalog offers every group and value in a preset. */
export function permissionPresetAvailable(
  preset: ProviderPermissionPreset | undefined,
  groups: AvailableOptionGroup[] | undefined,
): preset is ProviderPermissionPreset {
  const entries = nonEmptyPresetEntries(preset)
  if (!entries)
    return false
  return entries.every(([groupId, value]) =>
    !!optionGroup(groups, groupId)?.mutable && valueValidForGroup(groups, groupId, value),
  )
}

/**
 * The presets the live catalog currently offers: a preset is usable only when the
 * catalog carries every axis it sets. The ONE implementation of the rule the
 * composer `[+]` menu's permission items and a control request's permission pill
 * group both follow, so the two surfaces cannot offer different preset sets.
 */
export function usablePresets(
  presets: ProviderPermissionPresets | undefined,
  groups: AvailableOptionGroup[] | undefined,
): ProviderPermissionPresets {
  const usable: ProviderPermissionPresets = {}
  for (const { kind } of PERMISSION_PRESET_SPECS) {
    const preset = presets?.[kind]
    if (permissionPresetAvailable(preset, groups))
      usable[kind] = preset
  }
  return usable
}
