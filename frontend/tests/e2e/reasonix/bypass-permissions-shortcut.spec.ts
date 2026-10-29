import { existsSync } from 'node:fs'
import { join, relative, sep } from 'node:path'
import { expect } from '@playwright/test'
import { REASONIX_APPROVAL, REASONIX_CONFIG } from '../../../src/generated/contracts/reasonix-protocol'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { exerciseMidTurnBypass } from '../helpers/nativeMidTurnBypass'
import { exerciseNativeToolWrite } from '../helpers/nativePermission'
import { currentNativeAgent, expectNativeOptionValue, nativeToolOutcome } from '../helpers/nativeScenario'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { writeToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { applyPermissionPreset, chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
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

for (const posture of ['ask', 'bypass'] as const) {
  reasonixTest(`refuses a write outside the native workspace scope under ${posture}`, async ({ native }) => {
    const { page } = native
    await chooseSettingsOption(page, 'permissionMode-normal')
    await chooseSettingsOption(page, 'tool_approval-ask')
    await waitForSettingsIdle(page)
    if (posture === 'bypass')
      await applyPermissionPreset(page, 'bypass')
    const owner = await currentNativeAgent(native)
    const directory = createTestDirectory('reasonix-protected-write-')
    expect(relative(owner.workingDir, directory).split(sep)[0], 'the private target must be outside the session workspace').toBe('..')
    const target = join(directory, 'protected.txt')
    const callId = `reasonix-protected-${posture}`
    await expectNoNativeControl(native, {
      testId: 'control-banner',
      relatedProof: async () => {
        const { resultRequest } = await runNativeToolTurn(native, {
          toolCalls: [
            writeToolCall(native.provider, callId, { path: target, content: 'The native workspace scope must protect this file.' }),
          ],
          prompt: 'Try the scripted write outside the workspace.',
          answer: 'The native workspace refused the write.',
        })
        expect((await nativeToolOutcome(native, resultRequest, callId)).text).toContain('is outside the workspace write scope')
      },
    })
    expect(existsSync(target)).toBe(false)
    await expectNativeOptionValue(native, REASONIX_CONFIG.ToolApproval, posture === 'bypass'
      ? REASONIX_APPROVAL.Yolo
      : REASONIX_APPROVAL.Ask)
    await page.reload()
    expect(existsSync(target)).toBe(false)
  })
}
