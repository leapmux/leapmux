import type { Accessor } from 'solid-js'
import type { PermissionPresetController, ProviderPermissionPresets } from '../providerSettings'
import type { ActionsProps } from './types'
import type { PillOptions, PillOptionSpec } from '~/components/common/PillGroup'
import type { PermissionMode } from '~/utils/controlResponse'

import { Minus } from 'lucide-solid'
import { OPTION_ID_PERMISSION_MODE } from '~/components/chat/settingsGroups'
import { isPillOptions } from '~/components/common/PillGroup'
import { PERMISSION_PRESET_LABELS } from '../providerSettings'
import { createControlChoice } from './types'

/**
 * What the permission pill group does when the request's positive action is taken.
 *
 * `unspecified` applies NO permission change. It is not a "default mode" the
 * agent switches to -- it leaves every axis where the session already has it.
 */
export type PermissionPresetChoice
  = | 'unspecified'
    | 'smart'
    | 'bypass'

/** The answer-state key the permission pill group's choice is stored under. */
const CONTROL_PERMISSION_CHOICE_ID = 'control-permissions-pill'

/**
 * The choice a PLAN APPROVAL opens on.
 *
 * Approving a plan is the moment the user hands the agent the work, so the
 * banner offers Smart rather than asking for it again on the first tool call.
 * A provider whose catalog offers no smart preset draws no smart pill, and
 * `buildPermissionPill` clamps this to `unspecified` there.
 */
export const PLAN_APPROVAL_PERMISSION_CHOICE: PermissionPresetChoice = 'smart'

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
  // text: the other two name a preset the user is choosing. `Minus` reads as
  // "no change"; the label stays as the accessible name and the tooltip.
  const options: PillOptionSpec<PermissionPresetChoice>[] = [{ key: 'unspecified', label: 'Unchanged', icon: Minus }]
  for (const kind of ['smart', 'bypass'] as const) {
    if (presets[kind])
      options.push({ key: kind, label: PERMISSION_PRESET_LABELS[kind].short })
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

/**
 * Applies the chosen preset after a control request's positive answer — the same
 * `onSettingChange({ sets })` call the composer `[+]` menu's permission items make,
 * so a pill and the menu item cannot diverge in what they switch.
 *
 * The CALLER must await the answer before calling this. The worker dispatches the
 * response and the settings change concurrently, and applying a permission mode the
 * provider cannot take live relaunches the agent -- a relaunch that won the race
 * killed the session before the answer reached it. `Unchanged`, a missing controller
 * or a preset the catalog stopped offering applies nothing.
 */
export function applyPermissionPreset(
  presets: PermissionPresetController | undefined,
  choice: PermissionPresetChoice,
): Promise<void> {
  const preset = choice === 'unspecified' ? undefined : presets?.[choice]
  if (!presets || !preset)
    return Promise.resolve()
  return Promise.resolve(presets.apply({ sets: { ...preset.sets } }))
}

/**
 * The permission mode a PLAN APPROVAL attaches to its allow envelope: the chosen
 * preset's own mode axis, or nothing for `Unchanged`.
 *
 * Plan approvals switch the mode INSIDE the control response rather than through a
 * separate settings change: the worker applies it atomically with the approval, and
 * a context-clearing approval restarts the agent targeted at exactly this mode -- a
 * follow-up settings RPC would race that restart. A preset that switches no
 * permission mode (Copilot's approval axes) attaches nothing here, and no provider
 * that offers a plan-approval banner has such a preset today.
 */
export function presetPermissionMode(
  presets: ProviderPermissionPresets | undefined,
  choice: PermissionPresetChoice,
): PermissionMode | undefined {
  return choice === 'unspecified' ? undefined : presets?.[choice]?.sets[OPTION_ID_PERMISSION_MODE]
}

/**
 * The presets a PLAN APPROVAL banner may offer: only those that switch the
 * permission mode, the one axis its single response can carry. The result carries
 * no `apply` handler — a plan approval switches the mode inside its response and
 * never fires a settings change, and the narrower type keeps that true at compile
 * time. A preset that switches some other axis (Copilot's `allow_all`) cannot act
 * there at all, so its pill is not drawn -- drawn and silently doing nothing is the
 * trap the plan banner's old bypass switch was narrowed to avoid. No provider with
 * a plan-approval banner ships such a preset today; this keeps the rule true if one
 * ever does.
 */
export function planApprovalPresets(presets: PermissionPresetController | undefined): ProviderPermissionPresets | undefined {
  if (!presets)
    return undefined
  const smart = presets.smart?.sets[OPTION_ID_PERMISSION_MODE] !== undefined ? presets.smart : undefined
  const bypass = presets.bypass?.sets[OPTION_ID_PERMISSION_MODE] !== undefined ? presets.bypass : undefined
  return smart || bypass ? { smart, bypass } : undefined
}
