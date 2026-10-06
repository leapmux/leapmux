import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, expectSettingsChip, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'

kimiTest.describe('applies Kimi Code session settings', () => {
  kimiTest('switches the effort and mode in native turns, and keeps them after a reload', async ({ native, authenticatedKimiWorkspace }) => {
    const { page, modelScript } = native
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'GLM-5.3 Flash')
    await expectSettingsChip(page, 'High')

    await chooseSettingsOption(page, 'effort-low')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Low')

    const lowStep = await modelScript.queue({ text: 'The low effort turn ended.' })
    await sendMessage(page, modelScript.prompt('Answer once at low effort.'))
    await modelScript.waitForSteps(lowStep + 1)
    await waitForAgentIdle(page)
    expect((await modelScript.requestAt(lowStep)).body).toHaveProperty('reasoning_effort', 'low')

    await chooseSettingsOption(page, 'permissionMode-yolo')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Ask When Needed')

    // Kimi treats removal of one file as routine. Forced recursive removal of
    // this test directory reaches the Ask When Needed permission check.
    const directory = join(authenticatedKimiWorkspace.workingDir, 'mode-dangerous-proof')
    const removal = 'rm -r -f mode-dangerous-proof'
    mkdirSync(directory)
    writeFileSync(join(directory, 'keep.txt'), 'keep this file\n')
    await exerciseNativePermissionDecision(native, {
      toolCall: bashToolCall(AgentProvider.KIMI_CODE, 'mode-delete', removal),
      decision: 'deny',
      beforeDecision: banner => expect(banner).toContainText('mode-dangerous-proof'),
      nativeProof: () => expect(existsSync(directory)).toBe(true),
    })

    await chooseSettingsOption(page, 'swarmMode-on')
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, 'swarmMode-on')

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Low')
    await expectSettingsChip(page, 'Ask When Needed')
    await expectSettingsOptionChosen(page, 'swarmMode-on')

    await exerciseNativePermissionDecision(native, {
      toolCall: bashToolCall(AgentProvider.KIMI_CODE, 'mode-delete-restored', removal),
      decision: 'deny',
      beforeDecision: banner => expect(banner).toContainText('mode-dangerous-proof'),
      nativeProof: (restored) => {
        expect(nativeToolResult(restored, 'mode-delete-restored')).toContain('was not run because the user rejected the approval request')
        expect(restored.body).toHaveProperty('reasoning_effort', 'low')
        expect(nativeModelInstructionText(restored)).toContain('You are now in "agent swarm" mode.')
        expect(existsSync(directory)).toBe(true)
      },
    })
  })
})
