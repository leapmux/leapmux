import type { PermissionPresetController } from '../providerSettings'
import { describe, expect, it, vi } from 'vitest'
import {
  PLAN_APPROVAL_PERMISSION_CHOICE,
  planApprovalPresets,
  presetPermissionMode,
} from './planApproval'

const SMART = { sets: { permissionMode: 'auto' } }
const BYPASS = { sets: { permissionMode: 'bypassPermissions' } }
const COPILOT_SMART = { sets: { copilot_assisted_approval: 'on' } }
const COPILOT_BYPASS = { sets: { allow_all: 'on' } }

function controller(partial: Partial<PermissionPresetController> = {}): PermissionPresetController {
  return { smart: SMART, bypass: BYPASS, apply: vi.fn(), ...partial }
}

describe('plan approval permission presets', () => {
  it('opens on Smart', () => {
    expect(PLAN_APPROVAL_PERMISSION_CHOICE).toBe('smart')
  })

  it('reads the mode from the chosen preset and nothing from Unchanged', () => {
    expect(presetPermissionMode(controller(), 'bypass')).toBe('bypassPermissions')
    expect(presetPermissionMode(controller(), 'smart')).toBe('auto')
    expect(presetPermissionMode(controller(), 'unspecified')).toBeUndefined()
  })

  it('attaches nothing for a preset that switches another axis', () => {
    expect(presetPermissionMode(controller({ smart: COPILOT_SMART, bypass: COPILOT_BYPASS }), 'bypass')).toBeUndefined()
  })

  it('keeps only the presets that carry a permission mode', () => {
    const filtered = planApprovalPresets(controller())!
    expect(filtered.smart).toBe(SMART)
    expect(filtered.bypass).toBe(BYPASS)
    expect('apply' in filtered).toBe(false)
  })

  it('drops a preset with no permission mode and keeps the other', () => {
    const filtered = planApprovalPresets(controller({ smart: COPILOT_SMART }))!
    expect(filtered.smart).toBeUndefined()
    expect(filtered.bypass).toBe(BYPASS)
  })

  it('offers nothing when no preset carries a permission mode', () => {
    expect(planApprovalPresets(controller({ smart: COPILOT_SMART, bypass: COPILOT_BYPASS }))).toBeUndefined()
    expect(planApprovalPresets(undefined)).toBeUndefined()
  })
})
