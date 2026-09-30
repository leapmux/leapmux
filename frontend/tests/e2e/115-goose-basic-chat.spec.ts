import { existsSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { expect, GOOSE_E2E_SKIP_REASON, gooseTest } from './goose-fixtures'
import { nativeToolResult } from './helpers/nativeToolResult'
import { bashToolCall } from './helpers/providerToolCalls'
import { applyPermissionPreset, ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, openPlusMenu, openSettingsMenu, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated } from './helpers/ui'

gooseTest.skip(!!GOOSE_E2E_SKIP_REASON, GOOSE_E2E_SKIP_REASON || '')

gooseTest.describe('Goose Basic Chat', () => {
  gooseTest('send message and receive response', async ({ authenticatedGooseWorkspace, page, modelScript }) => {
    void authenticatedGooseWorkspace
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await waitForAgentIdle(page, 120_000)
    await expectAssistantAnswer(page)
  })

  gooseTest('permission shortcuts switch Smart Approve and Auto', async ({ authenticatedGooseWorkspace, page }) => {
    void authenticatedGooseWorkspace
    await waitForSettingsHydrated(page)
    const menu = await openPlusMenu(page)
    // A new Goose session starts in Smart Approve, so the Smart shortcut is already
    // applied and therefore disabled.
    await expect(menu.getByTestId('composer-smart-permissions')).toBeDisabled()

    await applyPermissionPreset(page, 'bypass')
    let group = await openSettingsMenu(page, 'permissionMode')
    await expect(group.locator('[data-testid="permissionMode-auto"] input[type="radio"]')).toBeChecked()

    await applyPermissionPreset(page, 'smart')
    group = await openSettingsMenu(page, 'permissionMode')
    await expect(group.locator('[data-testid="permissionMode-smart_approve"] input[type="radio"]')).toBeChecked()
  })

  gooseTest('smart mode asks before a removal and auto mode runs it', async ({ authenticatedGooseWorkspace, page, modelScript }) => {
    const workingDir = authenticatedGooseWorkspace.workingDir
    if (!workingDir)
      throw new Error('the Goose workspace has no working directory')
    const marker = join(workingDir, 'goose-mode-marker.txt')
    writeFileSync(marker, 'keep this file\n')

    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.GOOSE, 'goose-smart-remove', 'rm -f goose-mode-marker.txt && printf goose-mode-42')] },
      { text: 'The Smart check ended.' },
    )
    await sendMessage(page, modelScript.prompt('Try the scripted removal under Smart Approve.'))
    await modelScript.waitForSteps(1)
    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('goose-mode-marker.txt')
    await page.getByTestId('control-deny-btn').filter({ visible: true }).click()
    await modelScript.waitForSteps(2)
    await waitForAgentIdle(page)
    expect(existsSync(marker)).toBe(true)

    await applyPermissionPreset(page, 'bypass')
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.GOOSE, 'goose-auto-remove', 'rm -f goose-mode-marker.txt && printf goose-mode-42')] },
      { text: 'The Auto check ended.' },
    )
    await sendMessage(page, modelScript.prompt('Run the scripted removal under Auto.'))
    const status = await modelScript.waitForSteps(4)
    await waitForAgentIdle(page)
    await expect(banner).toHaveCount(0)
    expect(existsSync(marker)).toBe(false)
    const result = nativeToolResult(status.requests.find(request => request.stepIndex === 3), 'goose-auto-remove')
    expect(result).toContain('goose-mode-42')
  })
})
