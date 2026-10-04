import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { QODER_MODE } from '../../../src/generated/contracts/qoder-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, closeComposerMenus, openPlusMenu, openSettingsMenu, sendMessage, settingsGroupTrigger, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { expect, expectQoderModeChip, QODER_E2E_SKIP_REASON, qoderTest } from '../qoder-fixtures'

qoderTest.describe('Qoder CLI settings', () => {
  qoderTest.skip(!!QODER_E2E_SKIP_REASON, QODER_E2E_SKIP_REASON || '')

  qoderTest('the mode menu lists the five modes, and a switch survives a reload', async ({ qoderWorkspace, page }) => {
    void qoderWorkspace
    await waitForSettingsHydrated(page)

    const group = await openSettingsMenu(page, 'permissionMode')
    for (const testId of [
      `permissionMode-${QODER_MODE.Default}`,
      `permissionMode-${QODER_MODE.AcceptEdits}`,
      `permissionMode-${QODER_MODE.Auto}`,
      `permissionMode-${QODER_MODE.DontAsk}`,
      `permissionMode-${QODER_MODE.Plan}`,
    ]) {
      await expect(group.locator(`[data-testid="${testId}"] input[type="radio"]`)).toBeVisible()
    }
    // The fixture opens the agent in Accept Edits.
    await expect(group.locator(`[data-testid="permissionMode-${QODER_MODE.AcceptEdits}"] input[type="radio"]`)).toBeChecked()
    await closeComposerMenus(page)

    await chooseSettingsOption(page, `permissionMode-${QODER_MODE.Auto}`)
    await waitForSettingsIdle(page)
    await expectQoderModeChip(page, 'Auto')

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectQoderModeChip(page, 'Auto')
    const after = await openSettingsMenu(page, 'permissionMode')
    await expect(after.locator(`[data-testid="permissionMode-${QODER_MODE.Auto}"] input[type="radio"]`)).toBeChecked()
    await closeComposerMenus(page)
    // The model axis is present; the account decides which models it lists.
    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'model')).toBeVisible()
    await closeComposerMenus(page)
  })

  qoderTest('asks in Default and denies a write in Don\'t Ask', async ({ askingQoderWorkspace, page, modelScript }) => {
    const file = join(askingQoderWorkspace.workingDir, 'qoder-mode-write.txt')
    const command = 'printf qoder-mode-write > ./qoder-mode-write.txt'
    await waitForSettingsHydrated(page)
    await expectQoderModeChip(page, 'Default')

    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.QODER, 'default-mode-write', command)] },
      { text: 'The Default decision was recorded.' },
    )
    await sendMessage(page, modelScript.prompt('Try the requested write in Default mode.'))
    await modelScript.waitForSteps(1)
    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText('qoder-mode-write.txt')
    expect(existsSync(file)).toBe(false)
    await page.getByTestId('control-deny-btn').filter({ visible: true }).click()
    const defaultStatus = await modelScript.waitForSteps(2)
    await waitForAgentIdle(page)
    expect(nativeToolResult(defaultStatus.requests.find(request => request.stepIndex === 1), 'default-mode-write')).toMatch(/denied|rejected|not allowed/i)
    expect(existsSync(file)).toBe(false)

    await chooseSettingsOption(page, `permissionMode-${QODER_MODE.DontAsk}`)
    await waitForSettingsIdle(page)
    await expectQoderModeChip(page, 'Don\'t Ask')
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.QODER, 'dont-ask-mode-write', command)] },
      { text: 'The Dont Ask decision was recorded.' },
    )
    await sendMessage(page, modelScript.prompt('Try the same write without asking.'))
    const deniedStatus = await modelScript.waitForSteps(4)
    await waitForAgentIdle(page)
    await expect(banner).toHaveCount(0)
    expect(existsSync(file)).toBe(false)
    expect(nativeToolResult(deniedStatus.requests.find(request => request.stepIndex === 3), 'dont-ask-mode-write'))
      .toContain('the "Don\'t ask" permission mode does not prompt')
  })
})
