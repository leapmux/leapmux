import type { PermissionPresetController } from '../providerSettings'
import type { PermissionPresetChoice } from './permissionPresets'
import type { ActionsProps } from './types'
import { Minus } from 'lucide-solid'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import {
  applyPermissionPreset,
  buildPermissionPill,
  createPermissionPresetChoice,
  permissionPillOptions,
  planApprovalPresets,
  presetPermissionMode,
  sessionPermissionChoice,
} from './permissionPresets'
import { createControlAnswerState } from './types'

const SMART = { sets: { permissionMode: 'auto' } }
const BYPASS = { sets: { permissionMode: 'bypassPermissions' } }
const COPILOT_SMART = { sets: { copilot_assisted_approval: 'on' } }
const COPILOT_BYPASS = { sets: { allow_all: 'on' } }

function controller(partial: Partial<PermissionPresetController> = {}): PermissionPresetController {
  return { smart: SMART, bypass: BYPASS, apply: vi.fn(), ...partial }
}

/** A choice state with nothing stored, so it reports the opening choice alone. */
function openingChoice(opening: PermissionPresetChoice) {
  return createPermissionPresetChoice({ answerState: createControlAnswerState() }, () => opening)
}

describe('permissionPillOptions', () => {
  it('offers Unchanged first, then each preset the controller carries', () => {
    // `unspecified` applies no permission change at all, which is what the
    // label says. It is not a "default mode" the agent switches to.
    expect(permissionPillOptions(controller())).toEqual([
      { key: 'unspecified', label: 'Unchanged', icon: Minus },
      { key: 'smart', label: 'Smart' },
      { key: 'bypass', label: 'Bypass' },
    ])
  })

  it('omits a preset the controller does not carry', () => {
    // ZCode and Codex ship no smart preset; their group is Default + Bypass.
    expect(permissionPillOptions(controller({ smart: undefined }))).toEqual([
      { key: 'unspecified', label: 'Unchanged', icon: Minus },
      { key: 'bypass', label: 'Bypass' },
    ])
  })

  it('offers no group without presets', () => {
    expect(permissionPillOptions(undefined)).toBeUndefined()
    expect(permissionPillOptions(controller({ smart: undefined, bypass: undefined }))).toBeUndefined()
  })
})

describe('buildPermissionPill', () => {
  it('opens on the choice its surface supplies', () => {
    expect(buildPermissionPill(controller(), openingChoice('smart'))!.selected).toBe('smart')
    expect(buildPermissionPill(controller(), openingChoice('bypass'))!.selected).toBe('bypass')
    expect(buildPermissionPill(controller(), openingChoice('unspecified'))!.selected).toBe('unspecified')
  })

  it('clamps an opening choice the group draws no pill for', () => {
    // A plan approval opens on smart whether or not the provider ships one.
    // Codex and ZCode draw Unchanged + Bypass, and Bypass must never arrive as
    // an opening choice, so the group falls back to the pill that applies
    // nothing.
    const pill = buildPermissionPill(controller({ smart: undefined }), openingChoice('smart'))!

    expect(pill.options.map(o => o.key)).toEqual(['unspecified', 'bypass'])
    expect(pill.selected).toBe('unspecified')
  })

  it('follows a moving opening choice until the user picks a pill', () => {
    // The session can switch mode under an open banner, and an untouched group
    // must move with it. The first click pins the choice.
    const props: Pick<ActionsProps, 'answerState'> = { answerState: createControlAnswerState() }
    const [opening, setOpening] = createSignal<PermissionPresetChoice>('unspecified')
    const choice = createPermissionPresetChoice(props, opening)

    expect(buildPermissionPill(controller(), choice)!.selected).toBe('unspecified')
    setOpening('bypass')
    expect(buildPermissionPill(controller(), choice)!.selected).toBe('bypass')

    buildPermissionPill(controller(), choice)!.onSelect('smart')
    setOpening('unspecified')
    expect(buildPermissionPill(controller(), choice)!.selected).toBe('smart')
    expect(props.answerState.choices()).toEqual({ 'control-permissions-pill': 'smart' })
  })

  it('builds nothing without presets', () => {
    expect(buildPermissionPill(undefined, openingChoice('smart'))).toBeUndefined()
  })

  it('clamps a stored choice the catalog no longer offers back to Unchanged', () => {
    // The catalog withdrew smart while the stored choice still says it: the
    // group must report Unchanged, not a selection with no radio to check it.
    const choice = createPermissionPresetChoice(
      { answerState: createControlAnswerState({ choices: { 'control-permissions-pill': 'smart' } }) },
      () => 'unspecified',
    )

    const pill = buildPermissionPill(controller({ smart: undefined }), choice)!

    expect(pill.options.map(o => o.key)).toEqual(['unspecified', 'bypass'])
    expect(pill.selected).toBe('unspecified')
  })
})

describe('applyPermissionPreset', () => {
  it('applies the chosen preset as one complete settings change', async () => {
    const apply = vi.fn()
    await applyPermissionPreset(controller({ apply }), 'bypass')
    expect(apply).toHaveBeenCalledWith({ sets: { permissionMode: 'bypassPermissions' } })

    await applyPermissionPreset(controller({ apply }), 'smart')
    expect(apply).toHaveBeenCalledWith({ sets: { permissionMode: 'auto' } })
  })

  it('applies nothing for Unchanged, a missing controller, or a withdrawn preset', async () => {
    const apply = vi.fn()
    await applyPermissionPreset(controller({ apply }), 'unspecified')
    await applyPermissionPreset(undefined, 'bypass')
    // The catalog stopped offering smart while the stored choice still says it.
    await applyPermissionPreset(controller({ apply, smart: undefined }), 'smart')
    expect(apply).not.toHaveBeenCalled()
  })
})

describe('presetPermissionMode', () => {
  it('reads the mode off the chosen preset and nothing off Unchanged', () => {
    expect(presetPermissionMode(controller(), 'bypass')).toBe('bypassPermissions')
    expect(presetPermissionMode(controller(), 'smart')).toBe('auto')
    expect(presetPermissionMode(controller(), 'unspecified')).toBeUndefined()
  })

  it('attaches nothing for a preset that switches some other axis', () => {
    // Copilot's presets are not permission modes at all.
    expect(presetPermissionMode(controller({ smart: COPILOT_SMART, bypass: COPILOT_BYPASS }), 'bypass')).toBeUndefined()
  })
})

describe('planApprovalPresets', () => {
  it('keeps only the presets a plan approval response can act on', () => {
    const filtered = planApprovalPresets(controller())!
    expect(filtered.smart).toBe(SMART)
    expect(filtered.bypass).toBe(BYPASS)
    // No `apply` travels with the plan view: the mode rides inside the
    // response, and the narrower type keeps a settings change out of it.
    expect('apply' in filtered).toBe(false)
  })

  it('drops a preset with no permission mode, keeping the other', () => {
    const filtered = planApprovalPresets(controller({ smart: COPILOT_SMART }))!
    expect(filtered.smart).toBeUndefined()
    expect(filtered.bypass).toBe(BYPASS)
  })

  it('offers nothing when no preset carries a mode', () => {
    expect(planApprovalPresets(controller({ smart: COPILOT_SMART, bypass: COPILOT_BYPASS }))).toBeUndefined()
    expect(planApprovalPresets(undefined)).toBeUndefined()
  })
})

describe('sessionPermissionChoice', () => {
  it('reports the preset the session already has on', () => {
    expect(sessionPermissionChoice(controller({ active: 'smart' }))).toBe('smart')
    expect(sessionPermissionChoice(controller({ active: 'bypass' }))).toBe('bypass')
  })

  it('changes nothing when neither preset is on', () => {
    // An ordinary request must never turn a preset ON by itself. It opens on
    // the running one, or on the pill that applies nothing.
    expect(sessionPermissionChoice(controller())).toBe('unspecified')
    expect(sessionPermissionChoice(undefined)).toBe('unspecified')
  })
})
