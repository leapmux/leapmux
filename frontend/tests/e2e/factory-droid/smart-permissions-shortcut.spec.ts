import type { MockModelToolCall } from '../helpers/mockModelScript'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { DROID_TOOL } from '../../../src/generated/contracts/droid-protocol'
import { droidTest } from '../droid-fixtures'
import { expectTurnEndedAfter } from '../helpers/modelScriptFixture'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { droidExecuteToolCall, editToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { answerControl, applyPermissionPreset, chooseSettingsOption, expectNoControlBanner, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForControlBanner, waitForNativeSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { expectDroidNativeSettings, readDroidNativeSettings } from './settingsUpdates'
import { readDroidToolResult } from './toolResult'

droidTest('uses native medium autonomy after Bypass startup and restores approval in Default', async ({ native }) => {
  const { page } = native
  await waitForNativeSettingsHydrated(page)
  const before = await currentNativeAgent(native)
  const initial = await readDroidNativeSettings(native, before.id)
  expect(initial.modelId).toBeTruthy()
  await expectDroidNativeSettings(native, { interactionMode: 'auto', autonomyLevel: 'high' })
  await applyPermissionPreset(page, 'smart')
  await waitForSettingsIdle(page)

  const note = join(before.workingDir, 'droid-smart-note.txt')
  writeFileSync(note, 'before')
  for (const pass of [0, 1]) {
    if (pass === 1) {
      await page.reload()
      await waitForNativeSettingsHydrated(page)
    }
    await expectSettingsOptionChosen(page, 'permissionMode-auto-medium')
    await expectDroidNativeSettings(native, { interactionMode: 'auto', autonomyLevel: 'medium' })
    const settings = await readDroidNativeSettings(native, before.id)
    expect(settings.modelId).toBe(initial.modelId)
    expect(settings.reasoningEffort).toBe(initial.reasoningEffort)
    expect((await currentNativeAgent(native)).agentSessionId).toBe(before.agentSessionId)

    const oldText = pass === 0 ? 'before' : 'first edit'
    const newText = pass === 0 ? 'first edit' : 'second edit'
    const callId = `smart-edit-${pass}`
    await expectNoNativeControl(native, { testId: 'control-banner', relatedProof: async () => {
      const { resultRequest } = await runNativeToolTurn(native, {
        toolCalls: [editToolCall(native.provider, callId, { path: note, before: oldText, after: newText })],
        prompt: 'Apply the scripted edit under native Smart permissions.',
        answer: `The Smart edit completed in pass ${pass}.`,
        permissions: 'none',
      })
      expect(readDroidToolResult(resultRequest, callId, DROID_TOOL.Edit).text).toContain('has been updated successfully.')
      expect(readFileSync(note, 'utf8')).toBe(newText)
    } })

    const mediumTarget = join(before.workingDir, `droid-smart-medium-${pass}.txt`)
    expect(existsSync(mediumTarget)).toBe(false)
    const mediumId = `smart-medium-${pass}`
    const mediumCommand = `node -e ${quotePosixShellArgument(`require("node:fs").writeFileSync(${JSON.stringify(mediumTarget)}, "SMART" + (40 + 2))`)}`
    await expectNoNativeControl(native, { testId: 'control-banner', relatedProof: async () => {
      const { resultRequest } = await runNativeToolTurn(native, {
        toolCalls: [droidExecuteToolCall(mediumId, { command: mediumCommand, summary: 'Write a private medium-risk marker', riskLevel: 'medium' })],
        prompt: 'Run the scripted medium-risk command under Smart permissions.',
        answer: `The medium-risk command completed in pass ${pass}.`,
        permissions: 'none',
      })
      expect(readDroidToolResult(resultRequest, mediumId, DROID_TOOL.Execute).exitCode).toBe(0)
      expect(readFileSync(mediumTarget, 'utf8')).toBe('SMART42')
    } })

    const target = join(before.workingDir, `droid-smart-high-${pass}.txt`)
    const command = `node -e ${quotePosixShellArgument(`require("node:fs").writeFileSync(${JSON.stringify(target)}, "blocked")`)}`
    await rejectDroidTool(native, droidExecuteToolCall(`smart-high-${pass}`, { command, summary: 'Write a private approval marker', riskLevel: 'high' }), command, () => expect(existsSync(target)).toBe(false))
  }

  await chooseSettingsOption(page, 'permissionMode-default')
  await waitForSettingsIdle(page)
  await expectDroidNativeSettings(native, { interactionMode: 'auto', autonomyLevel: 'off' })
  await rejectDroidTool(native, editToolCall(native.provider, 'smart-restored-default', { path: note, before: 'second edit', after: 'unexpected edit' }), 'Edit', () => expect(readFileSync(note, 'utf8')).toBe('second edit'))
  expect((await currentNativeAgent(native)).agentSessionId).toBe(before.agentSessionId)
  const restored = await readDroidNativeSettings(native, before.id)
  expect(restored.modelId).toBe(initial.modelId)
  expect(restored.reasoningEffort).toBe(initial.reasoningEffort)
})

/** Droid ends a refused turn without a second model request. */
async function rejectDroidTool(native: ManagedNativeScenarioContext, toolCall: MockModelToolCall, bannerText: string, expectUnchanged: () => void): Promise<void> {
  const start = await native.modelScript.queue({ toolCalls: [toolCall] })
  await sendMessage(native.page, native.modelScript.prompt('Request the scripted operation that needs approval.'))
  await native.modelScript.waitForSteps(start + 1)
  const banner = await waitForControlBanner(native.page)
  await expect(banner).toContainText(bannerText)
  expectUnchanged()
  await answerControl(native.page, 'deny')
  await waitForAgentIdle(native.page)
  await expectNoControlBanner(native.page)
  expectUnchanged()
  await expectTurnEndedAfter(native.modelScript, start + 1)
}
