import { describe, expect, it } from 'vitest'
import { MUSE_APPROVAL_MODE } from '~/generated/contracts/muse-protocol'
import { musePermissionPresets } from './permissionPresets'

describe('musePermissionPresets', () => {
  it('selects the native allowAll mode for bypass', () => {
    expect(musePermissionPresets.bypass.sets).toEqual({ permissionMode: MUSE_APPROVAL_MODE.AllowAll })
  })
})
