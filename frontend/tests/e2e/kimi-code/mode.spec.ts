import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, closeComposerMenus, expectSettingsChip, openSettingsMenu, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'

kimiTest.describe('applies Kimi Code session settings', () => {
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

    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.KIMI_CODE, 'mode-delete-restored', 'rm -r -f mode-dangerous-proof')] },
      { text: 'The restored permission check ended.' },
    )
    await sendMessage(page, modelScript.prompt('Repeat the actual removal under the restored permission setting.'))
    await modelScript.waitForSteps(4)
    const restoredBanner = await waitForControlBanner(page)
    await expect(restoredBanner).toContainText('mode-dangerous-proof')
    await page.getByTestId('control-deny-btn').filter({ visible: true }).click()
    const restoredStatus = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const restored = restoredStatus.requests.find(request => request.stepIndex === 4)
    if (!restored)
      throw new Error('The restored Kimi permission turn produced no native result.')
    expect(nativeToolResult(restored, 'mode-delete-restored')).toContain('was not run because the user rejected the approval request')
    expect(restored.body).toHaveProperty('reasoning_effort', 'low')
    expect(nativeModelInstructionText(restored)).toContain('You are now in "agent swarm" mode.')
    expect(existsSync(directory)).toBe(true)
  })
})
