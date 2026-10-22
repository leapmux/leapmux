import { describe, expect, it } from 'vitest'
import { OPTION_ID_PERMISSION_MODE } from '../../settingsGroups'
import { juniePermissionPresets } from './permissionPresets'

describe('juniePermissionPresets', () => {
  it('selects Brave Auto for smart and Brave On for bypass', () => {
    // The literals pin Junie's wire vocabulary, so a typo in JUNIE_BRAVE cannot pass.
    expect(juniePermissionPresets.smart?.sets).toEqual({ brave_mode: 'brave-auto' })
    expect(juniePermissionPresets.bypass?.sets).toEqual({ brave_mode: 'on' })
  })

  it('never touches the permission-mode axis', () => {
    // Brave mode and the session mode are independent axes, so Plan and a
    // preset stay on together.
    expect(Object.keys(juniePermissionPresets.smart?.sets ?? {})).not.toContain(OPTION_ID_PERMISSION_MODE)
    expect(Object.keys(juniePermissionPresets.bypass?.sets ?? {})).not.toContain(OPTION_ID_PERMISSION_MODE)
  })
})
