import type { AvailableOptionGroup } from '~/generated/proto/leapmux/v1/agent_pb'
import { optionGroup, resolvedCurrent, valueValidForGroup } from './settingsGroups'

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

/** The standard permission presets that a provider can offer. */
export interface ProviderPermissionPresets {
  smart?: ProviderPermissionPreset
  bypass?: ProviderPermissionPreset
}

/** What one standard preset is called, in each of the two contexts that offer it. */
export interface PermissionPresetLabel {
  /**
   * The name for a surface that already states the subject. The control
   * request's pill group is titled `Permissions`, so an option that repeated
   * the word said it twice and pushed the row to wrap mid-word.
   */
  short: string
  /**
   * The name for a surface that states no subject of its own. The composer
   * `[+]` menu draws its permission items as bare rows under a rule, so each
   * one must say what it acts on.
   */
  full: string
}

/**
 * The names each standard preset shows, wherever a preset is offered: the composer
 * `[+]` menu's permission items and a control request's permission pill group read
 * this one table, so the two surfaces cannot drift apart on a rename.
 */
export const PERMISSION_PRESET_LABELS: Record<keyof ProviderPermissionPresets, PermissionPresetLabel> = {
  smart: { short: 'Smart', full: 'Smart permissions' },
  bypass: { short: 'Bypass', full: 'Bypass permissions' },
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
  /**
   * The preset the session already has on, or `undefined` when neither is on.
   *
   * A control request's pill group opens on this, so an Allow keeps the session
   * where the user put it instead of changing it. Only the composer knows it,
   * because it needs the live catalog AND the confirmed values, and a control
   * request's props carry neither.
   */
  active?: keyof ProviderPermissionPresets
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
  if (!preset)
    return false
  const entries = Object.entries(preset.sets)
  // An empty change sets nothing, so "already applied" would be vacuously true.
  if (entries.length === 0)
    return false
  return entries.every(([groupId, value]) => resolvedCurrent(groups, values, groupId) === value)
}

/**
 * Which standard preset the session has on, smart before bypass.
 *
 * The order settles the one case where both can report active: Copilot's two
 * presets switch DIFFERENT axes, so both can be on at once. Claude's, Codex's
 * and Goose's switch the same permission mode and exclude each other.
 */
export function activePermissionPreset(
  presets: ProviderPermissionPresets,
  groups: AvailableOptionGroup[] | undefined,
  values: Record<string, string> | undefined,
): keyof ProviderPermissionPresets | undefined {
  for (const kind of ['smart', 'bypass'] as const) {
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
  if (!preset)
    return false
  const entries = Object.entries(preset.sets)
  if (entries.length === 0)
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
  return {
    smart: permissionPresetAvailable(presets?.smart, groups) ? presets?.smart : undefined,
    bypass: permissionPresetAvailable(presets?.bypass, groups) ? presets?.bypass : undefined,
  }
}
