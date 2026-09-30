import { CODEBUDDY_EFFORT_LEVEL, CODEBUDDY_MODE } from '../../src/generated/contracts/codebuddy-protocol'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEBUDDY_E2E_SKIP_REASON, codebuddyTest, expect } from './codebuddy-fixtures'
import { CODEBUDDY_ALT_MODEL_ID, CODEBUDDY_ALT_MODEL_WIRE_ID } from './helpers/mockAgentEnvironment'
import { bashToolCall, exitPlanModeToolCall } from './helpers/providerToolCalls'
import {
  applyPermissionPreset,
  chooseSettingsOption,
  closeComposerMenus,
  expectSettingsChip,
  openPlusMenu,
  openSettingsMenu,
  sendMessage,
  settingsGroupTrigger,
  waitForAgentIdle,
  waitForControlBanner,
  waitForSettingsHydrated,
  waitForSettingsIdle,
} from './helpers/ui'

/**
 * 280 — CodeBuddy Code settings.
 *
 * CodeBuddy advertises four permission modes at startup (note 12 of the
 * feature matrix): Default, Accept Edits, Plan and Bypass Permissions. The
 * effort axis carries CodeBuddy's own six words, not Claude's set. The spec
 * lists the mode menu, switches the effort and the mode, and proves that a
 * reload keeps both.
 */
codebuddyTest.skip(!!CODEBUDDY_E2E_SKIP_REASON, CODEBUDDY_E2E_SKIP_REASON || '')

codebuddyTest.describe('CodeBuddy Code settings', () => {
  codebuddyTest('switches the model for the next native request', async ({ codebuddyWorkspace, page, modelScript }) => {
    void codebuddyWorkspace
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, `model-${CODEBUDDY_ALT_MODEL_ID}`)
    await waitForSettingsIdle(page)

    await modelScript.queue({ text: 'The alternate model answered.' })
    await sendMessage(page, modelScript.prompt('Reply once with the alternate model.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const body = status.requests.find(request => request.stepIndex === 0)?.body
    if (!body || typeof body !== 'object' || !('model' in body))
      throw new Error('the CodeBuddy model request must state its model')
    expect(body.model).toBe(CODEBUDDY_ALT_MODEL_WIRE_ID)

    await page.reload()
    const group = await openSettingsMenu(page, 'model')
    await expect(group.getByTestId(`model-${CODEBUDDY_ALT_MODEL_ID}`)).toHaveAttribute('aria-checked', 'true')
    await closeComposerMenus(page)
  })

  codebuddyTest('offers Bypass but no Smart shortcut and runs a native tool after Bypass', async ({ codebuddyWorkspace, page, modelScript }) => {
    void codebuddyWorkspace
    await waitForSettingsHydrated(page, 'permissionMode')
    await chooseSettingsOption(page, `permissionMode-${CODEBUDDY_MODE.Default}`)
    await waitForSettingsIdle(page)
    const defaultModes = await openSettingsMenu(page, 'permissionMode')
    await expect(defaultModes.locator(`[data-testid="permissionMode-${CODEBUDDY_MODE.Default}"] input[type="radio"]`)).toBeChecked()
    await closeComposerMenus(page)

    const menu = await openPlusMenu(page)
    await expect(menu.getByTestId('composer-smart-permissions')).toHaveCount(0)
    await expect(menu.getByTestId('composer-bypass-permissions')).toBeVisible()
    await closeComposerMenus(page)
    await applyPermissionPreset(page, 'bypass')
    await expectSettingsChip(page, 'Bypass Permissions')

    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.CODEBUDDY, 'bypass-proof', 'echo "codebuddy-bypass-$((40 + 2))"')] },
      { text: 'The bypass command ended.' },
    )
    await sendMessage(page, modelScript.prompt('Run the scripted command after Bypass.'))
    await modelScript.waitForSteps(1)
    await expect(page.getByTestId('control-banner').filter({ visible: true })).toHaveCount(0)
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const messages = (status.requests.find(request => request.stepIndex === 1)?.body as { messages?: { role?: string, content?: unknown }[] } | undefined)?.messages ?? []
    const result = messages.findLast(message => message.role === 'tool')?.content
    expect(JSON.stringify(result)).toContain('codebuddy-bypass-42')
  })

  codebuddyTest('the mode menu lists the four advertised modes', async ({ codebuddyWorkspace, page }) => {
    void codebuddyWorkspace
    await waitForSettingsHydrated(page)

    const group = await openSettingsMenu(page, 'permissionMode')
    for (const testId of [
      `permissionMode-${CODEBUDDY_MODE.Default}`,
      `permissionMode-${CODEBUDDY_MODE.AcceptEdits}`,
      `permissionMode-${CODEBUDDY_MODE.Plan}`,
      `permissionMode-${CODEBUDDY_MODE.BypassPermissions}`,
    ]) {
      await expect(group.locator(`[data-testid="${testId}"] input[type="radio"]`)).toBeVisible()
    }
    // The fixture opens the agent in Bypass Permissions.
    await expect(group.locator(`[data-testid="permissionMode-${CODEBUDDY_MODE.BypassPermissions}"] input[type="radio"]`)).toBeChecked()
    await closeComposerMenus(page)
  })

  codebuddyTest('switches the effort and the mode, and keeps them after a reload', async ({ codebuddyWorkspace, page }) => {
    void codebuddyWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Bypass Permissions')
    // The model axis is present; the account and the local model table decide
    // which models it lists, so the spec asserts the group and not a choice.
    await openSettingsMenu(page, 'model')
    await closeComposerMenus(page)

    await chooseSettingsOption(page, `effort-${CODEBUDDY_EFFORT_LEVEL.Low}`)
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Low')

    await chooseSettingsOption(page, `permissionMode-${CODEBUDDY_MODE.AcceptEdits}`)
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Accept Edits')

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Low')
    await expectSettingsChip(page, 'Accept Edits')
    const group = await openSettingsMenu(page, 'permissionMode')
    await expect(group.locator(`[data-testid="permissionMode-${CODEBUDDY_MODE.AcceptEdits}"] input[type="radio"]`)).toBeChecked()
    await closeComposerMenus(page)
    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'model')).toBeVisible()
    await closeComposerMenus(page)
  })

  codebuddyTest('sends a selected effort in the next native request', async ({ codebuddyWorkspace, page, modelScript }) => {
    void codebuddyWorkspace
    await modelScript.queue({ text: 'The first effort probe answered.' })
    await sendMessage(page, modelScript.prompt('Reply before I change effort.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await chooseSettingsOption(page, `effort-${CODEBUDDY_EFFORT_LEVEL.Low}`)
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Low')
    await modelScript.queue({ text: 'The low-effort probe answered.' })
    await sendMessage(page, modelScript.prompt('Reply after I change effort.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const next = status.requests.find(request => request.stepIndex === 1)
    expect(next?.protocol).toBe('openai-chat-completions')
    if (!next?.body || typeof next.body !== 'object' || !('reasoning_effort' in next.body))
      throw new Error('the CodeBuddy model request must state its selected effort')
    expect(next.body.reasoning_effort).toBe('low')
    expect(JSON.stringify(next.body).includes('The first effort probe answered.')).toBe(true)
  })

  codebuddyTest('keeps selected Plan mode for the next native turn', async ({ codebuddyWorkspace, page, modelScript }) => {
    void codebuddyWorkspace
    await modelScript.queue({ text: 'The first mode probe answered.' })
    await sendMessage(page, modelScript.prompt('Reply before I change mode.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await chooseSettingsOption(page, `permissionMode-${CODEBUDDY_MODE.Plan}`)
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Plan')
    await modelScript.queue({ toolCalls: [exitPlanModeToolCall(AgentProvider.CODEBUDDY, 'exit-selected-plan', '# Probe plan')] })
    await modelScript.fallback({ text: 'The mode probe ended.' })
    await sendMessage(page, modelScript.prompt('Present the plan for review after the mode change.'))
    const status = await modelScript.waitForSteps()
    const next = status.requests.find(request => request.stepIndex === 1)
    expect(next?.protocol).toBe('openai-chat-completions')
    expect(JSON.stringify(next?.body).includes('The first mode probe answered.')).toBe(true)
    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('Plan Ready for Review')
    await page.getByTestId('plan-reject-btn').click()
    await waitForAgentIdle(page)
    await expectSettingsChip(page, 'Plan')
  })
})
