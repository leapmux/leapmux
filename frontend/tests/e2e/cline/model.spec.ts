import type { Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CLINE_E2E_SKIP_REASON, clineTest } from '../cline-fixtures'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { chooseSettingsOption, closeComposerMenus, expectSettingsChip, openPlusMenu, openSettingsMenu, sendMessage, settingsGroupTrigger, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

/**
 * The selected model must reach an actual native request. The setting must survive a page reload.
 *
 * The Worker starts one private Cline hub for this agent. Cline's DeepSeek provider sends requests to the isolated mock.
 *
 * Cline's hub exposes no model catalog. The Worker combines its native provider catalog with the configured custom model.
 */
clineTest.skip(!!CLINE_E2E_SKIP_REASON, CLINE_E2E_SKIP_REASON || '')

/** The option ids the mode group offers. */
async function modeOptions(page: Page): Promise<string[]> {
  const group = await openSettingsMenu(page, 'permissionMode')
  // Each option also holds a label element whose id ends `-label`.
  const ids = await group.locator('[data-testid^="permissionMode-"]:not([data-testid$="-label"])').evaluateAll(nodes => nodes.map(node => node.getAttribute('data-testid') ?? ''))
  await closeComposerMenus(page)
  return ids
}

clineTest.describe('Cline settings', () => {
  clineTest('switches to a model from the native provider catalog', async ({ askingClineWorkspace, page, modelScript }) => {
    void askingClineWorkspace
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, 'model-deepseek-v4-pro')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'DeepSeek V4 Pro')

    await modelScript.queue({ text: 'The selected model answered.' })
    await sendMessage(page, modelScript.prompt('Reply once with the selected model.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    const body = status.requests.find(request => request.stepIndex === 0)?.body
    if (!body || typeof body !== 'object' || !('model' in body))
      throw new Error('the Cline model request must state its model')
    expect(body.model).toBe('deepseek-v4-pro')

    await page.reload()
    const group = await openSettingsMenu(page, 'model')
    await expect(group.locator('[data-testid="model-deepseek-v4-pro"] input[type="radio"]')).toBeChecked()
    await closeComposerMenus(page)
    const restored = await sendNativeAnswer({ page, modelScript, provider: AgentProvider.CLINE }, 'Reply after restoring the catalog model.', 'The restored catalog model answered.')
    expect(restored.body).toHaveProperty('model', 'deepseek-v4-pro')
  })

  clineTest('offers the configured model, the three modes, and no effort for a model without a ladder', async ({ askingClineWorkspace, page, modelScript }) => {
    void askingClineWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, MOCK_MODELS.cline)
    await expectSettingsChip(page, 'Act')
    expect(await modeOptions(page)).toEqual(['permissionMode-plan', 'permissionMode-act', 'permissionMode-auto_approve'])

    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'model')).toBeVisible()
    await expect(settingsGroupTrigger(page, 'effort')).toHaveCount(0)
    await closeComposerMenus(page)

    await modelScript.queue({ text: 'The configured model answered.' })
    await sendMessage(page, modelScript.prompt('Reply once with the configured model.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    const body = status.requests.find(request => request.stepIndex === 0)?.body
    if (!body || typeof body !== 'object' || !('model' in body))
      throw new Error('the Cline model request must state its model')
    expect(body.model).toBe(MOCK_MODELS.cline)
  })
})
