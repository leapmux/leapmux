import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { writeToolCall } from '../helpers/providerToolCalls'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, chooseSettingsOption, closeComposerMenus, expectAssistantAnswer, expectSettingsChip, openSettingsMenu, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { expect, LETTA_E2E_SKIP_REASON, LETTA_TITLE_RULE, lettaTest } from '../letta-fixtures'

lettaTest.describe('Letta Code modes', () => {
  lettaTest.skip(!!LETTA_E2E_SKIP_REASON, LETTA_E2E_SKIP_REASON || '')

  lettaTest('the mode menu lists Standard, Accept Edits, Unrestricted and Strict', async ({ authenticatedLettaWorkspace, page }) => {
    void authenticatedLettaWorkspace
    await waitForSettingsHydrated(page)

    const mode = await openSettingsMenu(page, 'permissionMode')
    await expect(mode.locator('[data-testid="permissionMode-standard"] input[type="radio"]')).toBeVisible()
    await expect(mode.locator('[data-testid="permissionMode-acceptEdits"] input[type="radio"]')).toBeVisible()
    await expect(mode.locator('[data-testid="permissionMode-unrestricted"] input[type="radio"]')).toBeVisible()
    await expect(mode.locator('[data-testid="permissionMode-strict"] input[type="radio"]')).toBeVisible()
    await closeComposerMenus(page)
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

  lettaTest('asks in Standard and runs a write in Unrestricted', async ({ askingLettaWorkspace, page, modelScript }) => {
    const file = join(askingLettaWorkspace.workingDir, 'letta-mode-proof.txt')
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Standard')
    await modelScript.rule(LETTA_TITLE_RULE)

    await modelScript.queue(
      { toolCalls: [writeToolCall(AgentProvider.LETTA, 'standard-mode-write', { path: file, content: 'standard\n' })] },
      { text: 'The Standard decision was recorded.' },
    )
    await sendMessage(page, modelScript.prompt('Try the requested write in Standard mode.'))
    await modelScript.waitForSteps(1)
    const banner = await waitForControlBanner(page)
    expect(existsSync(file)).toBe(false)
    await page.getByTestId('control-deny-btn').filter({ visible: true }).click()
    await modelScript.waitForSteps(2)
    await waitForAgentIdle(page)
    expect(existsSync(file)).toBe(false)
    await expect(banner).toHaveCount(0)

    await chooseSettingsOption(page, 'permissionMode-unrestricted')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Unrestricted')
    await modelScript.queue(
      { toolCalls: [writeToolCall(AgentProvider.LETTA, 'unrestricted-mode-write', { path: file, content: 'unrestricted\n' })] },
      { text: 'The Unrestricted write finished.' },
    )
    await sendMessage(page, modelScript.prompt('Write the proof file without asking.'))
    const status = await modelScript.waitForSteps(4)
    await waitForAgentIdle(page)
    await expect(banner).toHaveCount(0)
    expect(readFileSync(file, 'utf8')).toBe('unrestricted\n')
    expect(nativeToolResult(status.requests.find(request => request.stepIndex === 3), 'unrestricted-mode-write'))
      .toContain('letta-mode-proof.txt')
  })
})

lettaTest.describe('Letta Code settings', () => {
  lettaTest.skip(!!LETTA_E2E_SKIP_REASON, LETTA_E2E_SKIP_REASON || '')

  lettaTest('applies a permission-mode change to the session', async ({ authenticatedLettaWorkspace, page, modelScript }) => {
    void authenticatedLettaWorkspace
    await waitForSettingsHydrated(page)
    await modelScript.rule(LETTA_TITLE_RULE)
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expectAssistantAnswer(page)

    // A settings change reaches the running session. The chip follows the value.
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, 'permissionMode-strict')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Strict')
  })
})
