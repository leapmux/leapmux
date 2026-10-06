import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { LETTA_MODE } from '../../../src/generated/contracts/letta-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { expectSettingsOptionsOffered } from '../helpers/nativeSettings'
import { nativeToolResultAt } from '../helpers/nativeToolExecution'
import { writeToolCall } from '../helpers/providerToolCalls'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, chooseSettingsOption, expectAssistantAnswer, expectNoControlBanner, expectSettingsChip, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

lettaTest.describe('Letta Code modes', () => {
  lettaTest('the mode menu lists Standard, Accept Edits, Unrestricted and Strict', async ({ authenticatedLettaWorkspace, page }) => {
    void authenticatedLettaWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsOptionsOffered(page, 'permissionMode', Object.values(LETTA_MODE))
  })

  lettaTest('a mode change reaches the chip and survives a reload', async ({ authenticatedLettaWorkspace, page }) => {
    void authenticatedLettaWorkspace
    await waitForSettingsHydrated(page)

    await chooseSettingsOption(page, 'permissionMode-standard')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Standard')

    await chooseSettingsOption(page, 'permissionMode-acceptEdits')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Accept Edits')

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Accept Edits')
  })

  lettaTest('asks in Standard and runs a write in Unrestricted', async ({ askingLettaWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingLettaWorkspace.workspaceId })
    const file = join(askingLettaWorkspace.workingDir, 'letta-mode-proof.txt')
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Standard')

    await exerciseNativePermissionDecision(context, {
      toolCall: writeToolCall(AgentProvider.LETTA, 'standard-mode-write', { path: file, content: 'standard\n' }),
      decision: 'deny',
      beforeDecision: () => expect(existsSync(file)).toBe(false),
      nativeProof: () => expect(existsSync(file)).toBe(false),
    })
    await expectNoControlBanner(page)

    await chooseSettingsOption(page, 'permissionMode-unrestricted')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Unrestricted')
    // Unrestricted runs the write without a banner, so the scenario answers no control.
    const unrestrictedStep = await modelScript.queue(
      { toolCalls: [writeToolCall(AgentProvider.LETTA, 'unrestricted-mode-write', { path: file, content: 'unrestricted\n' })] },
      { text: 'The Unrestricted write finished.' },
    )
    await sendMessage(page, modelScript.prompt('Write the proof file without asking.'))
    await modelScript.waitForSteps(unrestrictedStep + 2)
    await waitForAgentIdle(page)
    await expectNoControlBanner(page)
    expect(readFileSync(file, 'utf8')).toBe('unrestricted\n')
    expect(await nativeToolResultAt(modelScript, unrestrictedStep + 1, 'unrestricted-mode-write')).toContain('letta-mode-proof.txt')
  })
})

lettaTest.describe('Letta Code settings', () => {
  lettaTest('applies a permission-mode change to the session', async ({ authenticatedLettaWorkspace, page, modelScript }) => {
    void authenticatedLettaWorkspace
    await waitForSettingsHydrated(page)
    const step = await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps(step + 1)
    await waitForAgentIdle(page)
    await expectAssistantAnswer(page)

    // A settings change reaches the running session. The chip follows the value.
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, 'permissionMode-strict')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Strict')
  })
})
