import type { PermissionPresetController } from '../providerSettings'
import type { ActionsProps } from './types'
import { describe, expect, it, vi } from 'vitest'
import {
  applyPermissionPreset,
  buildPermissionPill,
  createPermissionPresetChoice,
  permissionPillOptions,
  planApprovalPresets,
  presetPermissionMode,
} from './permissionPresets'
import { createControlAnswerState } from './types'

const SMART = { sets: { permissionMode: 'auto' } }
const BYPASS = { sets: { permissionMode: 'bypassPermissions' } }
const COPILOT_SMART = { sets: { copilot_assisted_approval: 'on' } }
const COPILOT_BYPASS = { sets: { allow_all: 'on' } }

function controller(partial: Partial<PermissionPresetController> = {}): PermissionPresetController {
  return { smart: SMART, bypass: BYPASS, apply: vi.fn(), ...partial }
}

describe('permissionPillOptions', () => {
  it('offers Default first, then each preset the controller carries', () => {
    expect(permissionPillOptions(controller())).toEqual([
      { key: 'default', label: 'Default' },
      { key: 'smart', label: 'Smart permissions' },
      { key: 'bypass', label: 'Bypass permissions' },
    ])
  })

  it('omits a preset the controller does not carry', () => {
    // ZCode and Codex ship no smart preset; their group is Default + Bypass.
    expect(permissionPillOptions(controller({ smart: undefined }))).toEqual([
      { key: 'default', label: 'Default' },
      { key: 'bypass', label: 'Bypass permissions' },
    ])
  })

  it('offers no group without presets', () => {
    expect(permissionPillOptions(undefined)).toBeUndefined()
    expect(permissionPillOptions(controller({ smart: undefined, bypass: undefined }))).toBeUndefined()
  })
})

describe('buildPermissionPill', () => {
  it('renders the stored choice and writes selections back', () => {
    const props: Pick<ActionsProps, 'answerState'> = { answerState: createControlAnswerState() }
    const choice = createPermissionPresetChoice(props)

    const pill = buildPermissionPill(controller(), choice)!
    expect(pill.selected).toBe('default')

    pill.onSelect('bypass')
    expect(buildPermissionPill(controller(), choice)!.selected).toBe('bypass')
    expect(props.answerState.choices()).toEqual({ 'control-permissions-pill': 'bypass' })
  })

  it('builds nothing without presets', () => {
    const choice = createPermissionPresetChoice({ answerState: createControlAnswerState() })
    expect(buildPermissionPill(undefined, choice)).toBeUndefined()
  })

  it('clamps a stored choice the catalog no longer offers back to Default', () => {
    // The catalog withdrew smart while the stored choice still says it: the
    // group must report Default, not a selection with no radio to check it.
    const choice = createPermissionPresetChoice({
      answerState: createControlAnswerState({ choices: { 'control-permissions-pill': 'smart' } }),
    })

    const pill = buildPermissionPill(controller({ smart: undefined }), choice)!

    expect(pill.options.map(o => o.key)).toEqual(['default', 'bypass'])
    expect(pill.selected).toBe('default')
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

  it('applies nothing for Default, a missing controller, or a withdrawn preset', async () => {
    const apply = vi.fn()
    await applyPermissionPreset(controller({ apply }), 'default')
    await applyPermissionPreset(undefined, 'bypass')
    // The catalog stopped offering smart while the stored choice still says it.
    await applyPermissionPreset(controller({ apply, smart: undefined }), 'smart')
    expect(apply).not.toHaveBeenCalled()
  })
})

describe('presetPermissionMode', () => {
  it('reads the mode off the chosen preset and nothing off Default', () => {
    expect(presetPermissionMode(controller(), 'bypass')).toBe('bypassPermissions')
    expect(presetPermissionMode(controller(), 'smart')).toBe('auto')
    expect(presetPermissionMode(controller(), 'default')).toBeUndefined()
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
