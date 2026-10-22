import { expect } from '@playwright/test'
import { JUNIE_BRAVE } from '../../../src/components/chat/providers/junie/permissionPresets'
import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { nativeOptionValue } from '../helpers/nativeScenario'
import { junieTest } from '../junie-fixtures'

// The Bypass permissions shortcut selects Junie's Brave On (`on`), which executes
// potentially sensitive actions without approval.
junieTest('executes a protected native Bypass command under Brave On before and after reload', async ({ native }) => {
  await exerciseBypassPermissions(native, {
    settingsProof: (agent) => {
      expect(nativeOptionValue(agent, 'brave_mode')).toBe(JUNIE_BRAVE.On)
    },
  })
})
