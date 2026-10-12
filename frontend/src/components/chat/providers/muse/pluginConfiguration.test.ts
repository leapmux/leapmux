import { describe, expect, it } from 'vitest'
import { MUSE_OPTION_ID, MUSE_STARTUP_OPTION_GROUPS, MUSE_WORKSPACE_TRUST } from '~/generated/contracts/muse-protocol'
import { resolveStartupOptionValues, validateStartupOptionGroups } from '../startupOptions'
import { museConfiguration } from './pluginConfiguration'

describe('museConfiguration', () => {
  it('uses the generated startup groups and their native default', () => {
    const groups = museConfiguration.startupOptionGroups ?? []
    expect(groups).toEqual(Object.values(MUSE_STARTUP_OPTION_GROUPS))
    expect(() => validateStartupOptionGroups(groups)).not.toThrow()
    expect(resolveStartupOptionValues(groups, {})).toEqual({ [MUSE_OPTION_ID.WorkspaceTrust]: MUSE_WORKSPACE_TRUST.Native })
  })

  it('keeps the explicit trust choice separate from the native default', () => {
    const groups = museConfiguration.startupOptionGroups ?? []
    expect(resolveStartupOptionValues(groups, { [MUSE_OPTION_ID.WorkspaceTrust]: MUSE_WORKSPACE_TRUST.Agent }))
      .toEqual({ [MUSE_OPTION_ID.WorkspaceTrust]: MUSE_WORKSPACE_TRUST.Agent })
    expect(resolveStartupOptionValues(groups, {})).toEqual({ [MUSE_OPTION_ID.WorkspaceTrust]: MUSE_WORKSPACE_TRUST.Native })
  })

  it('rejects a foreign value before it reaches agent creation', () => {
    expect(() => resolveStartupOptionValues(museConfiguration.startupOptionGroups ?? [], { [MUSE_OPTION_ID.WorkspaceTrust]: 'futureTrust' })).toThrow('outside')
  })

  it('reports the native attachment policy and child input capability', () => {
    expect(museConfiguration.attachments).toEqual({ text: true, image: true, pdf: false, binary: false })
    expect(museConfiguration.supportsSubagentSend).toBe(true)
    expect(museConfiguration.triggerModeGroupKey).toBe('permissionMode')
  })
})
