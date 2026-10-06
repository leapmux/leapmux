import type { Page } from '@playwright/test'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { existsSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { GOOSE_MODE } from '../../../src/generated/contracts/goose-protocol'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { currentNativeAgent, expectNativeOptionValue, nativeTextStep } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall, goosePermissionJudgmentToolCall } from '../helpers/providerToolCalls'
import { uniqueMarker } from '../helpers/shellArguments'
import { applyPermissionPreset, expectNoControlBanner, expectPermissionShortcuts, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForNativeSettingsHydrated } from '../helpers/ui'

/**
 * Switch a new Goose session between its two permission shortcuts. A new session starts in Smart Approve, so its
 * Smart shortcut is disabled until the mode changes. Bypass selects Auto, and Smart selects Smart Approve again.
 */
export async function exerciseGooseShortcutSwitch(page: Page): Promise<void> {
  await waitForNativeSettingsHydrated(page)
  await expectPermissionShortcuts(page, { smart: 'disabled' })
  await applyPermissionPreset(page, 'bypass')
  await expectSettingsOptionChosen(page, `permissionMode-${GOOSE_MODE.Auto}`)
  await applyPermissionPreset(page, 'smart')
  await expectSettingsOptionChosen(page, `permissionMode-${GOOSE_MODE.SmartApprove}`)
}

/** Deny a real removal through Smart, then execute that removal through Auto. */
export async function exerciseGoosePermissionRemoval(context: ManagedNativeScenarioContext): Promise<void> {
  const { page, modelScript } = context
  const workingDir = (await currentNativeAgent(context)).workingDir
  if (!workingDir)
    throw new Error('the Goose workspace has no working directory')
  await applyPermissionPreset(page, 'bypass')
  await applyPermissionPreset(page, 'smart')
  await expectNativeOptionValue(context, 'permissionMode', GOOSE_MODE.SmartApprove)
  // Smart Approve asks Goose's permission-safety classifier about each tool call. The classifier lists no call as
  // read-only, so the removal raises a permission request.
  await modelScript.rule({ name: `goose-native-removal-judge-${uniqueMarker()}`, when: { system: 'permission-safety classifier' }, respond: { toolCalls: [goosePermissionJudgmentToolCall('goose-removal-judge', [])] } })
  const marker = join(workingDir, 'goose-mode-marker.txt')
  writeFileSync(marker, 'keep this file\n')

  await exerciseNativePermissionDecision(context, {
    toolCall: bashToolCall(context.provider, 'goose-smart-remove', 'rm -f goose-mode-marker.txt && printf goose-mode-42'),
    decision: 'deny',
    beforeDecision: banner => expect(banner).toContainText('goose-mode-marker.txt'),
    nativeProof: () => {
      expect(existsSync(marker)).toBe(true)
    },
  })

  await applyPermissionPreset(page, 'bypass')
  await expectNativeOptionValue(context, 'permissionMode', GOOSE_MODE.Auto)
  await expectNoNativeControl(context, { testId: 'control-banner', relatedProof: async () => {
    const start = await modelScript.queue(
      { toolCalls: [bashToolCall(context.provider, 'goose-auto-remove', 'rm -f goose-mode-marker.txt && printf goose-mode-42')] },
      nativeTextStep(context, 'The Auto check ended.'),
    )
    await sendMessage(page, modelScript.prompt('Run the scripted removal under Auto.'))
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    await expectNoControlBanner(page)
    expect(existsSync(marker)).toBe(false)
    expect(nativeToolResult(await modelScript.requestAt(start + 1), 'goose-auto-remove')).toContain('goose-mode-42')
  } })
}
