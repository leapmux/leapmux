import { QODER_MODE } from '../../src/generated/contracts/qoder-protocol'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { bashToolCall } from './helpers/providerToolCalls'
import {
  chooseSettingsOption,
  closeComposerMenus,
  expectSettingsChip,
  openPlusMenu,
  openSettingsMenu,
  sendMessage,
  settingsGroupTrigger,
  waitForAgentIdle,
  waitForSettingsHydrated,
  waitForSettingsIdle,
} from './helpers/ui'
import { expect, QODER_E2E_SKIP_REASON, qoderTest } from './qoder-fixtures'

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
    await expectSettingsChip(page, 'Auto')

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Auto')
    const after = await openSettingsMenu(page, 'permissionMode')
    await expect(after.locator(`[data-testid="permissionMode-${QODER_MODE.Auto}"] input[type="radio"]`)).toBeChecked()
    await closeComposerMenus(page)
    // The model axis is present; the account decides which models it lists.
    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'model')).toBeVisible()
    await closeComposerMenus(page)
  })

  // Note 26 of the feature matrix: a read-only shell command needs no prompt in
  // the default mode. The command prints a number its own text does not state.
  qoderTest('runs a read-only shell command with no banner in Default mode', async ({ askingQoderWorkspace, page, modelScript }) => {
    void askingQoderWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Default')

    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.QODER, 'read-only', 'echo "qoder-readonly-$((40 + 2))"')] },
      { text: 'The command ran.' },
    )
    await sendMessage(page, modelScript.prompt('Run the echo command.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expect(page.locator('[data-testid="control-banner"]')).toHaveCount(0)
    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: 'qoder-readonly-42' }).first()).toBeVisible()
  })
})
