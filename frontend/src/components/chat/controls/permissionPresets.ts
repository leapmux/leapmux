import type { Accessor } from 'solid-js'
import type { PermissionPresetController, PermissionPresetKind, ProviderPermissionPresets } from '../providerSettings'
import type { ActionsProps } from './types'
import type { PillOptions, PillOptionSpec } from '~/components/common/PillGroup'

import { Minus } from 'lucide-solid'
import { isPillOptions } from '~/components/common/PillGroup'
import { PERMISSION_PRESET_SPECS } from '../providerSettings'
import { createControlChoice } from './types'

/**
 * What the permission pill group does when the request's positive action is taken.
 *
 * `unspecified` applies NO permission change. It is not a "default mode" the
 * agent switches to -- it leaves every axis where the session already has it.
 */
export type PermissionPresetChoice
  = | 'unspecified'
    | PermissionPresetKind

/** The answer-state key the permission pill group's choice is stored under. */
const CONTROL_PERMISSION_CHOICE_ID = 'control-permissions-pill'

/**
 * The choice an ORDINARY permission request opens on: whichever preset the
 * session already has on.
 *
 * A request that arrives while the session runs on bypass opens on Bypass, so
 * an Allow keeps the session where the user put it. Neither preset on means
 * `unspecified`, which changes nothing. This never TURNS ON a preset the user
 * did not choose -- it reports the one already running.
 */
export function sessionPermissionChoice(presets: PermissionPresetController | undefined): PermissionPresetChoice {
  return presets?.active ?? 'unspecified'
}

export interface PermissionPresetChoiceState {
  choice: Accessor<PermissionPresetChoice>
  setChoice: (choice: PermissionPresetChoice) => void
}

/**
 * Binds the permission pill group's selection to the composer's shared answer
 * record, so a rebuild of the control component cannot discard it.
 *
 * `opening` supplies the choice for as long as the user picks no pill, and it is
 * an ACCESSOR because the session can move under an open banner -- a mode switch
 * from the composer must move the untouched group with it. A stored choice wins
 * over it from the first click onward.
 *
 * `createControlChoice` stores strings; every value this group writes is a
 * `PermissionPresetChoice` key (its pills carry no other keys), so the read-back
 * cast cannot surface a foreign string.
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
 * The pill group's options: `Unchanged` first (the do-nothing state), then each
 * preset the live catalog offers. A preset the catalog does not
 * offer has no pill, mirroring the composer menu's hide-when-unavailable rule; when
 * NEITHER is offered there is no group at all, so a provider without presets (Pi,
 * OpenCode, Cursor, ...) draws the row it drew before the group existed.
 */
export function permissionPillOptions(presets: ProviderPermissionPresets | undefined): PillOptions<PermissionPresetChoice> | undefined {
  if (!presets)
    return undefined
  // An icon, not the word. This group shares one composer footer row with the
  // decision buttons, and the do-nothing option is the one that can give up its
  // text: the other options identify a preset. `Minus` reads as
  // "no change"; the label stays as the accessible name and the tooltip.
  const options: PillOptionSpec<PermissionPresetChoice>[] = [{ key: 'unspecified', label: 'Unchanged', icon: Minus }]
  for (const spec of PERMISSION_PRESET_SPECS) {
    if (presets[spec.kind])
      options.push({ key: spec.kind, label: spec.shortLabel })
  }
  // A lone Unchanged pill is no choice at all: presets the catalog stopped
  // offering draw no group, same as no presets at all.
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
  // The choice is clamped to the offered pills: a preset the catalog stopped
  // offering while its choice was stored must not leave a group with no radio
  // checked and an Allow that silently applies nothing. This also catches an
  // OPENING choice the group draws no pill for -- a plan approval opens on
  // `smart` whether or not the provider ships one. `unspecified` is the clamp
  // target because it is the one pill every group draws, and it applies nothing.
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
 * Applies the chosen preset after a control request's positive answer — the same
 * `onSettingChange({ sets })` call the composer `[+]` menu's permission items make,
 * so a pill and the menu item cannot diverge in what they switch.
 *
 * The CALLER must await the answer before calling this. The worker dispatches the
 * response and the settings change concurrently, and applying a permission mode the
 * provider cannot take live relaunches the agent -- a relaunch that won the race
 * killed the session before the answer reached it. `Unchanged`, a missing
 * apply handler, or a preset the catalog stopped offering applies nothing.
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
 * Sends the permission response before it applies the chosen preset.
 *
 * Some providers relaunch for a permission change. The response must reach the
 * old session before that relaunch starts.
 */
export async function respondThenApplyPermissionPreset(
  response: Promise<void>,
  presets: PermissionPresetController | undefined,
  choice: PermissionPresetChoice,
): Promise<void> {
  await response
  await applyPermissionPreset(presets, choice)
}
