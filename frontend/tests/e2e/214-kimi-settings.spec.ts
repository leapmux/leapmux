import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { KIMI_MOCK_MODELS, MOCK_MODELS } from './helpers/mockAgentEnvironment'
import { exerciseProviderSteer } from './helpers/providerSteer'
import { bashToolCall } from './helpers/providerToolCalls'
import { applyPermissionPreset, chooseSettingsOption, closeComposerMenus, expectNoSettingsChip, expectSettingsChip, openPlusMenu, openSettingsMenu, sendMessage, settingsGroupTrigger, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated, waitForSettingsIdle } from './helpers/ui'
import { expect, KIMI_E2E_SKIP_REASON, kimiTest } from './kimi-fixtures'

kimiTest.skip(!!KIMI_E2E_SKIP_REASON, KIMI_E2E_SKIP_REASON || '')

/** The model identifier each answered request asked for. */
function requestedModels(status: { requests: { body: unknown }[] }): string[] {
  return status.requests.map(request => String((request.body as { model?: unknown }).model ?? ''))
}

kimiTest.describe('applies Kimi Code session settings', () => {
  kimiTest('steers a queued message into the active turn', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    void authenticatedKimiWorkspace
    await waitForSettingsHydrated(page)
    await applyPermissionPreset(page, 'bypass')
    await exerciseProviderSteer(page, modelScript, AgentProvider.KIMI_CODE)
  })

  kimiTest('switches the effort and mode in native turns, and keeps them after a reload', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'GLM-5.3 Flash')
    await expectSettingsChip(page, 'High')

    await chooseSettingsOption(page, 'effort-low')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Low')

    await modelScript.queue({ text: 'The low effort turn ended.' })
    await sendMessage(page, modelScript.prompt('Answer once at low effort.'))
    const lowStatus = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const lowRequest = lowStatus.requests.find(request => request.stepIndex === 0)
    expect((lowRequest?.body as { reasoning_effort?: unknown } | undefined)?.reasoning_effort).toBe('low')

    await chooseSettingsOption(page, 'permissionMode-yolo')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Ask When Needed')

    // Kimi treats removal of one file as routine. Forced recursive removal of
    // this test directory reaches the Ask When Needed permission check.
    const directory = join(authenticatedKimiWorkspace.workingDir, 'mode-dangerous-proof')
    mkdirSync(directory)
    writeFileSync(join(directory, 'keep.txt'), 'keep this file\n')
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.KIMI_CODE, 'mode-delete', 'rm -r -f mode-dangerous-proof')] },
      { text: 'The mode check ended.' },
    )
    await sendMessage(page, modelScript.prompt('Attempt the scripted delete in Ask When Needed mode.'))
    await modelScript.waitForSteps(2)
    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('mode-dangerous-proof')
    await page.getByTestId('control-deny-btn').filter({ visible: true }).click()
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    expect(existsSync(directory)).toBe(true)

    await chooseSettingsOption(page, 'swarmMode-on')
    await waitForSettingsIdle(page)
    const swarm = await openSettingsMenu(page, 'swarmMode')
    await expect(swarm.locator('[data-testid="swarmMode-on"]')).toHaveAttribute('aria-checked', 'true')
    await closeComposerMenus(page)

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Low')
    await expectSettingsChip(page, 'Ask When Needed')
    const swarmAfter = await openSettingsMenu(page, 'swarmMode')
    await expect(swarmAfter.locator('[data-testid="swarmMode-on"]')).toHaveAttribute('aria-checked', 'true')
    await closeComposerMenus(page)
  })

  // The second model thinks at no level, so the effort axis leaves with it.
  kimiTest('a model switch reaches the next request and drops the effort axis', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    void authenticatedKimiWorkspace
    await waitForSettingsHydrated(page)

    await chooseSettingsOption(page, `model-${KIMI_MOCK_MODELS.plain}`)
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'GLM-5.3')
    await expectNoSettingsChip(page, 'GLM-5.3 Flash')
    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'model')).toBeVisible()
    await expect(settingsGroupTrigger(page, 'effort')).toHaveCount(0)
    await closeComposerMenus(page)

    await modelScript.queue({ text: 'Answered on the plain model.' })
    await sendMessage(page, modelScript.prompt('Reply on the plain model.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    expect(requestedModels(status)).toEqual([MOCK_MODELS.pi])
  })
})
