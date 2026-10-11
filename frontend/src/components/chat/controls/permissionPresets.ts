import type { Accessor } from 'solid-js'
import type { PermissionPresetController, PermissionPresetKind, ProviderPermissionPresets } from '../providerSettings'
import type { ActionsProps } from './types'
import type { PillOptions, PillOptionSpec } from '~/components/common/pillOptions'

import { Minus } from 'lucide-solid'
import { isPillOptions } from '~/components/common/pillOptions'
import { PERMISSION_PRESET_SPECS } from '../providerSettings'
import { createControlChoice } from './types'

/**
 * Select the permission change that follows a positive reply.
 * The unspecified choice preserves every current session setting.
 */
export type PermissionPresetChoice
  = | 'unspecified'
    | PermissionPresetKind

/** The answer-state key the permission pill group's choice is stored under. */
const CONTROL_PERMISSION_CHOICE_ID = 'control-permissions-pill'

/**
 * An ordinary request opens with the session's active preset.
 * A Bypass session therefore opens with Bypass selected.
 * Without an active preset, use unspecified.
 * This initial choice reports the current preset and enables no new preset.
 */
export function sessionPermissionChoice(presets: PermissionPresetController | undefined): PermissionPresetChoice {
  return presets?.active ?? 'unspecified'
}

export interface PermissionPresetChoiceState {
  choice: Accessor<PermissionPresetChoice>
  setChoice: (choice: PermissionPresetChoice) => void
}

/**
 * Store this permission choice in the composer's shared answer record so a component rebuild preserves it.
 * Use the opening accessor until the user selects a choice.
 * A composer mode change can then update an untouched group.
 * A stored user choice takes precedence afterward.
 * Every key that this group writes belongs to PermissionPresetChoice.
 */
export function createPermissionPresetChoice(
  props: Pick<ActionsProps, 'answerState'>,
  opening: () => PermissionPresetChoice,
): PermissionPresetChoiceState {
  const { choice, setChoice } = createControlChoice(() => props.answerState, CONTROL_PERMISSION_CHOICE_ID)
  return {
    choice: () => (choice() as PermissionPresetChoice | undefined) ?? opening(),
    setChoice,
  }
}

/** Creates the permission choice for an ordinary request. */
export function createSessionPermissionPresetChoice(
  props: Pick<ActionsProps, 'answerState' | 'presets'>,
): PermissionPresetChoiceState {
  return createPermissionPresetChoice(props, () => sessionPermissionChoice(props.presets))
}

/**
 * List Unchanged first, then each preset that the live catalog offers.
 * Omit unavailable preset choices.
 * Omit the entire group when the catalog offers no presets.
 */
export function permissionPillOptions(presets: ProviderPermissionPresets | undefined): PillOptions<PermissionPresetChoice> | undefined {
  if (!presets)
    return undefined
  // Use the Minus icon for the Unchanged choice to keep room for the decision buttons.
  // Keep Unchanged as its accessible name and tooltip.
  // The other choices retain text that identifies their preset.
  const options: PillOptionSpec<PermissionPresetChoice>[] = [{ key: 'unspecified', label: 'Unchanged', icon: Minus }]
  for (const spec of PERMISSION_PRESET_SPECS) {
    if (presets[spec.kind])
      options.push({ key: spec.kind, label: spec.shortLabel })
  }
  // Omit a group that contains only Unchanged because it offers no preset choice.
  return options.length > 1 && isPillOptions(options) ? options : undefined
}

/** A render-ready permission pill group, or undefined when it draws no pills. */
export interface ControlPermissionPill {
  options: PillOptions<PermissionPresetChoice>
  selected: PermissionPresetChoice
  onSelect: (choice: PermissionPresetChoice) => void
}

export function buildPermissionPill(
  presets: ProviderPermissionPresets | undefined,
  choiceState: PermissionPresetChoiceState,
): ControlPermissionPill | undefined {
  const options = permissionPillOptions(presets)
  if (!options)
    return undefined
  // Use the stored choice only when the catalog still offers it.
  // Otherwise, select unspecified, which every displayed group offers and which applies no change.
  // This also handles a plan approval that initially selects Smart when that provider offers no Smart preset.
  const stored = choiceState.choice()
  const selected = options.some(option => option.key === stored) ? stored : 'unspecified'
  return { options, selected, onSelect: choiceState.setChoice }
}

/** Builds the pill for an ordinary request that can apply a settings change. */
export function buildSessionPermissionPill(
  presets: PermissionPresetController | undefined,
  choiceState: PermissionPresetChoiceState,
): ControlPermissionPill | undefined {
  return presets?.apply ? buildPermissionPill(presets, choiceState) : undefined
}

/**
 * Apply the selected preset through the same settings handler as the composer menu.
 * The caller must await the positive response first.
 * A provider can restart for a permission change, so an earlier settings change could stop the process before its reply arrives.
 * Apply no change for Unchanged, an absent handler, or a preset that the catalog no longer offers.
 */
export function applyPermissionPreset(
  presets: PermissionPresetController | undefined,
  choice: PermissionPresetChoice,
): Promise<void> {
  const preset = choice === 'unspecified' ? undefined : presets?.[choice]
  if (!presets?.apply || !preset)
    return Promise.resolve()
  return Promise.resolve(presets.apply({ sets: { ...preset.sets } }))
}

/**
 * Send the permission response before applying the selected preset.
 * A provider can restart for the settings change.
 * Its old session must receive the response before that restart.
 */
export async function respondThenApplyPermissionPreset(
  response: Promise<void>,
  presets: PermissionPresetController | undefined,
  choice: PermissionPresetChoice,
): Promise<void> {
  await response
  await applyPermissionPreset(presets, choice)
}
