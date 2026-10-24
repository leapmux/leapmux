import { existsSync } from 'node:fs'
import { join, relative, sep } from 'node:path'
import { expect } from '@playwright/test'
import { REASONIX_APPROVAL, REASONIX_CONFIG } from '../../../src/generated/contracts/reasonix-protocol'
import { exerciseMidTurnBypass } from '../helpers/nativeMidTurnBypass'
import { exerciseNativePermissionDecision, exerciseNativeToolWrite } from '../helpers/nativePermission'
import { currentNativeAgent, expectNativeOptionValue, nativeToolOutcome } from '../helpers/nativeScenario'
import { writeToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { applyPermissionPreset, chooseSettingsOption, waitForNativeSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { reasonixTest } from '../reasonix-fixtures'
import { exerciseReasonixSessionSettings } from './settingsScenario'

// Reasonix 1.38 asks to leave Plan mode after each Plan-mode answer, also under the Bypass preset. The settings scenario
// answers each of those requests, so no request stays open across the Bypass change and the reload.
reasonixTest('bypass-permissions-shortcut: applies Reasonix session settings and preserves them after reload', async ({ native }) => {
  await exerciseReasonixSessionSettings(native)
})

reasonixTest('runs a real native write without a permission request after the Bypass shortcut', async ({ native }) => {
  const { page } = native
  await chooseSettingsOption(page, 'permissionMode-normal')
  await chooseSettingsOption(page, 'tool_approval-ask')
  await waitForSettingsIdle(page)
  await applyPermissionPreset(page, 'bypass')
  await expectNativeOptionValue(native, REASONIX_CONFIG.ToolApproval, REASONIX_APPROVAL.Yolo)
  await exerciseNativeToolWrite(native, { permission: 'absent' })
})

reasonixTest('approves a pending tool and the next tool when Bypass changes during a turn', async ({ native }) => {
  await chooseSettingsOption(native.page, 'permissionMode-normal')
  await chooseSettingsOption(native.page, 'tool_approval-ask')
  await waitForSettingsIdle(native.page)
  await exerciseMidTurnBypass(native)
})

for (const enableBefore of [true, false]) {
  reasonixTest(`requires write access approval when Bypass is enabled ${enableBefore ? 'before the request' : 'while the request is pending'}`, async ({ native }) => {
    const { page } = native
    await chooseSettingsOption(page, 'permissionMode-normal')
    await chooseSettingsOption(page, 'tool_approval-ask')
    await waitForSettingsIdle(page)
    const owner = await currentNativeAgent(native)
    const directory = createTestDirectory('reasonix-protected-write-')
    expect(relative(owner.workingDir, directory).split(sep)[0], 'the private target must be outside the session workspace').toBe('..')
    const target = join(directory, 'protected.txt')
    expect(existsSync(target)).toBe(false)
    if (enableBefore) {
      await applyPermissionPreset(page, 'bypass')
      await waitForSettingsIdle(page)
    }
    const callId = `reasonix-protected-${enableBefore ? 'active' : 'pending'}`
    await exerciseNativePermissionDecision(native, {
      toolCall: writeToolCall(native.provider, callId, { path: target, content: 'This write needs a native human decision.' }),
      decision: 'deny',
      beforeDecision: async (banner) => {
        await expect(banner).toContainText(target)
        expect(existsSync(target)).toBe(false)
        if (!enableBefore) {
          await page.reload()
          await waitForNativeSettingsHydrated(page)
          await applyPermissionPreset(page, 'bypass')
          await waitForSettingsIdle(page)
        }
        await expectNativeOptionValue(native, REASONIX_CONFIG.ToolApproval, REASONIX_APPROVAL.Yolo)
        await expect(banner).toBeVisible()
        expect(existsSync(target)).toBe(false)
      },
      nativeProof: async (request) => {
        expect((await nativeToolOutcome(native, request, callId)).text).toContain('the user declined to extend write access')
        expect(existsSync(target)).toBe(false)
      },
    })
  })
}
