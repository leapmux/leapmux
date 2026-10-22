import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { JUNIE_BRAVE } from '../../../src/components/chat/providers/junie/permissionPresets'
import { OPTION_ID_PERMISSION_MODE } from '../../../src/components/chat/settingsGroups'
import { JUNIE_CONFIG, JUNIE_MODE } from '../../../src/generated/contracts/junie-protocol'
import { exerciseSmartPermissions } from '../helpers/nativeBypassPermissions'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { assertPrivateNativePath } from '../helpers/nativePrivatePath'
import { currentNativeAgent, expectNativeOptionValue, nativeOptionGroup, nativeOptionValue } from '../helpers/nativeScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { getGlobalState } from '../helpers/server'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { applyPermissionPreset, chooseSettingsOption, expectPermissionShortcuts, waitForNativeSettingsHydrated, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { junieTest } from '../junie-fixtures'

// Both shortcuts change Brave mode. Neither shortcut changes the session's Plan mode.
junieTest('switches the Brave mode axis between the Smart shortcut and the other values', async ({ native }) => {
  const { page } = native
  await waitForSettingsHydrated(page)
  const agent = await currentNativeAgent(native)
  expect(nativeOptionGroup(agent, JUNIE_CONFIG.BraveMode)?.mutable).toBe(true)
  expect(nativeOptionGroup(agent, JUNIE_CONFIG.BraveMode)?.options.map(option => option.id))
    .toEqual(expect.arrayContaining([JUNIE_BRAVE.Auto, JUNIE_BRAVE.On, JUNIE_BRAVE.Off]))
  // A shortcut that is already on disables itself, so each state follows the value the session starts in.
  const initial = nativeOptionValue(agent, JUNIE_CONFIG.BraveMode)
  await expectPermissionShortcuts(page, {
    smart: initial === JUNIE_BRAVE.Auto ? 'disabled' : 'offered',
    bypass: initial === JUNIE_BRAVE.On ? 'disabled' : 'offered',
  })

  await chooseSettingsOption(page, `${OPTION_ID_PERMISSION_MODE}-${JUNIE_MODE.Plan}`)
  await waitForSettingsIdle(page)
  await expectNativeOptionValue(native, OPTION_ID_PERMISSION_MODE, JUNIE_MODE.Plan)
  await chooseSettingsOption(page, `${JUNIE_CONFIG.BraveMode}-${JUNIE_BRAVE.Off}`)
  await waitForSettingsIdle(page)
  await expectNativeOptionValue(native, JUNIE_CONFIG.BraveMode, JUNIE_BRAVE.Off)

  await applyPermissionPreset(page, 'bypass')
  await expectNativeOptionValue(native, JUNIE_CONFIG.BraveMode, JUNIE_BRAVE.On)
  await expectNativeOptionValue(native, OPTION_ID_PERMISSION_MODE, JUNIE_MODE.Plan)
  await expectPermissionShortcuts(page, { smart: 'offered', bypass: 'disabled' })

  await applyPermissionPreset(page, 'smart')
  await expectNativeOptionValue(native, JUNIE_CONFIG.BraveMode, JUNIE_BRAVE.Auto)
  await expectPermissionShortcuts(page, { smart: 'disabled', bypass: 'offered' })
  await expectNativeOptionValue(native, OPTION_ID_PERMISSION_MODE, JUNIE_MODE.Plan)
  await page.reload()
  await waitForNativeSettingsHydrated(page)
  await expectNativeOptionValue(native, OPTION_ID_PERMISSION_MODE, JUNIE_MODE.Plan)
  await expectNativeOptionValue(native, JUNIE_CONFIG.BraveMode, JUNIE_BRAVE.Auto)
})

// Brave Auto approves a plain removal inside the workspace without an Agent Client Protocol permission request.
// A compound command requires Junie's allowlist model, which the mock server cannot answer.
// The same removal requests approval under Brave Off.
junieTest('runs by itself a native removal that Brave Off asks about, before and after reload', async ({ native }) => {
  const { page } = native
  const agent = await currentNativeAgent(native)
  assertPrivateNativePath(agent.workingDir, getGlobalState().tmpDir)
  const ask = join(agent.workingDir, 'native-smart-ask')
  mkdirSync(ask, { recursive: true })
  writeFileSync(join(ask, 'keep.txt'), 'Remove only through the denied native Smart command.\n')
  await chooseSettingsOption(page, `${JUNIE_CONFIG.BraveMode}-${JUNIE_BRAVE.Off}`)
  await waitForSettingsIdle(page)
  await expectNativeOptionValue(native, JUNIE_CONFIG.BraveMode, JUNIE_BRAVE.Off)
  await exerciseNativePermissionDecision(native, {
    toolCall: bashToolCall(native.provider, 'native-smart-ask', `rm -rf ${quotePosixShellArgument(ask)}`),
    decision: 'deny',
    beforeDecision: () => expect(existsSync(join(ask, 'keep.txt'))).toBe(true),
    nativeProof: (request) => {
      expect(existsSync(join(ask, 'keep.txt'))).toBe(true)
      expect(JSON.stringify(request.body)).toContain('Human rejected execution')
    },
  })
  await exerciseSmartPermissions(native, {
    settingsProof: () => expectNativeOptionValue(native, JUNIE_CONFIG.BraveMode, JUNIE_BRAVE.Auto),
    commandProof: target => ({
      command: `rm -rf ${quotePosixShellArgument(target)}`,
      resultProof: (outcome) => {
        expect(outcome.text).toContain('exit code 0')
      },
    }),
  })
})
