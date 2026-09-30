import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { QODER_MODE } from '../../src/generated/contracts/qoder-protocol'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { MOCK_MODELS, QODER_ALTERNATE_MODEL_ID } from './helpers/mockAgentEnvironment'
import { nativeToolResult } from './helpers/nativeToolResult'
import { bashToolCall } from './helpers/providerToolCalls'
import {
  applyPermissionPreset,
  chooseSettingsOption,
  closeComposerMenus,
  expectSettingsOptionChosen,
  openPlusMenu,
  openSettingsMenu,
  sendMessage,
  settingsGroupTrigger,
  waitForAgentIdle,
  waitForSettingsHydrated,
  waitForSettingsIdle,
} from './helpers/ui'
import { expect, expectQoderModeChip, QODER_E2E_SKIP_REASON, qoderTest } from './qoder-fixtures'

/**
 * 285 — Qoder CLI settings.
 *
 * Qoder offers five permission modes. The spec lists them, switches the mode
 * and proves a reload keeps it. The second case carries note 26 of the feature
 * matrix: in Default mode a read-only shell command runs with no banner, while
 * 252 proves that a write raises one.
 */
qoderTest.skip(!!QODER_E2E_SKIP_REASON, QODER_E2E_SKIP_REASON || '')

qoderTest.describe('Qoder CLI settings', () => {
  qoderTest('the Smart shortcut selects Auto mode', async ({ qoderWorkspace, page }) => {
    void qoderWorkspace
    await waitForSettingsHydrated(page, 'permissionMode')
    const menu = await openPlusMenu(page)
    await expect(menu.getByTestId('composer-smart-permissions')).toBeVisible()
    await closeComposerMenus(page)

    await applyPermissionPreset(page, 'smart')
    await expectQoderModeChip(page, 'Auto')
    const mode = await openSettingsMenu(page, 'permissionMode')
    await expect(mode.locator(`[data-testid="permissionMode-${QODER_MODE.Auto}"] input[type="radio"]`)).toBeChecked()
    await closeComposerMenus(page)
  })

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

  qoderTest('switches the model used by the next request', async ({ qoderWorkspace, page, modelScript }) => {
    void qoderWorkspace
    await waitForSettingsHydrated(page, 'model')
    await chooseSettingsOption(page, `model-${QODER_ALTERNATE_MODEL_ID}`)
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, `model-${QODER_ALTERNATE_MODEL_ID}`)

    await modelScript.queue({ text: 'The alternate model answered.' })
    await sendMessage(page, modelScript.prompt('Answer with the selected model.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    const request = status.requests.find(record => record.stepIndex === 0)
    expect(request?.body).toMatchObject({ model: MOCK_MODELS.qoder })
  })

  // Note 26 of the feature matrix: a safe read-only shell command needs no
  // prompt in Default mode. The result gives the agent's actual working directory.
  qoderTest('runs a read-only shell command with no banner in Default mode', async ({ askingQoderWorkspace, page, modelScript }) => {
    const { workingDir } = askingQoderWorkspace
    await waitForSettingsHydrated(page)
    await expectQoderModeChip(page, 'Default')

    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.QODER, 'read-only', 'pwd')] },
      { text: 'The command ran.' },
    )
    await sendMessage(page, modelScript.prompt('Run pwd.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expect(page.locator('[data-testid="control-banner"]')).toHaveCount(0)
    const followUp = status.requests.find(record => record.stepIndex === 1)
    expect(followUp?.body).toMatchObject({
      messages: expect.arrayContaining([
        expect.objectContaining({ role: 'tool', tool_call_id: 'read-only', content: workingDir }),
      ]),
    })
    await expect(page.locator('[data-testid="chat-container"]:visible').getByText(workingDir, { exact: false }).filter({ visible: true }).first()).toBeVisible()
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
