import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { JUNIE_BRAVE } from '../../../src/components/chat/providers/junie/permissionPresets'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { assertPrivateNativePath } from '../helpers/nativePrivatePath'
import { currentNativeAgent, expectNativeOptionValue, nativeOptionGroup, nativeOptionValue, nativeTextStep, nativeToolOutcome } from '../helpers/nativeScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { getGlobalState } from '../helpers/server'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { applyPermissionPreset, assistantBubbles, chooseSettingsOption, expectPermissionShortcuts, sendMessage, waitForAgentIdle, waitForNativeSettingsHydrated, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { junieTest } from '../junie-fixtures'

// The Smart permissions shortcut selects Junie's Brave Auto (`brave-auto`), never the
// plan/default mode axis, so the session mode stays what it was through the switch.
junieTest('switches the Brave mode axis between the Smart shortcut and the other values', async ({ native }) => {
  const { page } = native
  await waitForSettingsHydrated(page)
  const agent = await currentNativeAgent(native)
  expect(nativeOptionGroup(agent, 'brave_mode')?.mutable).toBe(true)
  expect(nativeOptionGroup(agent, 'brave_mode')?.options.map(option => option.id))
    .toEqual(expect.arrayContaining([JUNIE_BRAVE.Auto, JUNIE_BRAVE.On, JUNIE_BRAVE.Off]))
  // A shortcut that is already on disables itself, so each state follows the value the session starts in.
  const initial = nativeOptionValue(agent, 'brave_mode')
  await expectPermissionShortcuts(page, {
    smart: initial === JUNIE_BRAVE.Auto ? 'disabled' : 'offered',
    bypass: initial === JUNIE_BRAVE.On ? 'disabled' : 'offered',
  })

  await applyPermissionPreset(page, 'bypass')
  await expectNativeOptionValue(native, 'brave_mode', JUNIE_BRAVE.On)
  await expectPermissionShortcuts(page, { smart: 'offered', bypass: 'disabled' })

  await applyPermissionPreset(page, 'smart')
  await expectNativeOptionValue(native, 'brave_mode', JUNIE_BRAVE.Auto)
  await expectPermissionShortcuts(page, { smart: 'disabled', bypass: 'offered' })
})

// Brave Auto decides a plain removal inside the workspace by itself, with no ACP permission
// request. The shared Smart proof cannot serve because its compound removal-plus-printf
// command makes Junie consult its own allowlist model, which the mock server cannot answer.
// The same removal asks under Brave Off, and that contrast is the proof.
junieTest('runs by itself a native removal that Brave Off asks about, before and after reload', async ({ native }) => {
  const { page, modelScript } = native
  const agent = await currentNativeAgent(native)
  assertPrivateNativePath(agent.workingDir, getGlobalState().tmpDir)
  const ask = join(agent.workingDir, 'native-smart-ask')
  mkdirSync(ask, { recursive: true })
  writeFileSync(join(ask, 'keep.txt'), 'Remove only through the denied native Smart command.\n')
  await chooseSettingsOption(page, `brave_mode-${JUNIE_BRAVE.Off}`)
  await waitForSettingsIdle(page)
  await expectNativeOptionValue(native, 'brave_mode', JUNIE_BRAVE.Off)
  await exerciseNativePermissionDecision(native, {
    toolCall: bashToolCall(native.provider, 'native-smart-ask', `rm -rf ${quotePosixShellArgument(ask)}`),
    decision: 'deny',
    beforeDecision: () => expect(existsSync(join(ask, 'keep.txt'))).toBe(true),
    nativeProof: (request) => {
      expect(existsSync(join(ask, 'keep.txt'))).toBe(true)
      expect(JSON.stringify(request.body)).toContain('Human rejected execution')
    },
  })
  await applyPermissionPreset(page, 'smart')
  await waitForSettingsIdle(page)
  for (const reload of [false, true]) {
    // One agent session runs both passes. Each pass removes its own directory, so the second
    // pass cannot pass on what the first pass shows.
    const pass = Number(reload) + 1
    const target = join(agent.workingDir, `native-smart-safe-${pass}`)
    mkdirSync(target, { recursive: true })
    writeFileSync(join(target, 'keep.txt'), `Remove only through the isolated native Smart command of pass ${pass}.\n`)
    assertPrivateNativePath(target, getGlobalState().tmpDir)
    if (reload) {
      await page.reload()
      await waitForNativeSettingsHydrated(page)
    }
    // The shortcut's value survives the reload.
    await expectNativeOptionValue(native, 'brave_mode', JUNIE_BRAVE.Auto)
    await expectNoNativeControl(native, { testId: 'control-banner', relatedProof: async () => {
      const id = `native-smart-safe-${Number(reload)}`
      const answer = `The native Smart removal completed in pass ${pass}.`
      const start = await modelScript.queue(
        { toolCalls: [bashToolCall(native.provider, id, `rm -rf ${quotePosixShellArgument(target)}`)] },
        nativeTextStep(native, answer),
      )
      await sendMessage(page, modelScript.prompt('Run the scripted removal under Smart permissions.'))
      await modelScript.waitForSteps(start + 2)
      await waitForAgentIdle(page)
      expect(existsSync(target)).toBe(false)
      const request = await modelScript.requestAt(start + 1)
      expect((await nativeToolOutcome(native, request, id)).text).toContain('exit code 0')
      await expect(assistantBubbles(page).filter({ hasText: answer }).first()).toBeVisible()
    } })
  }
})
