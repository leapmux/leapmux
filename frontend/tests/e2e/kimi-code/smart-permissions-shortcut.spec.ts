import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { bashToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, expectNoControlBanner, expectSettingsChip, openPlusMenu, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'

kimiTest.describe('applies Kimi Code permission presets', () => {
  kimiTest('starts on Always Ask and offers both shortcuts', async ({ authenticatedKimiWorkspace, page }) => {
    void authenticatedKimiWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Always Ask')
    const menu = await openPlusMenu(page)
    await expect(menu.getByTestId('composer-smart-permissions')).toBeVisible()
    await expect(menu.getByTestId('composer-bypass-permissions')).toBeVisible()
    await page.keyboard.press('Escape')
  })

  kimiTest('the smart shortcut asks before a risky command and bypass runs it', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    await waitForSettingsHydrated(page)
    // Kimi treats removal of one file as routine. Forced recursive removal of
    // this test directory reaches the Smart permission check.
    const directory = join(authenticatedKimiWorkspace.workingDir, 'shortcut-dangerous-proof')
    mkdirSync(directory)
    writeFileSync(join(directory, 'keep.txt'), 'delete only in bypass\n')

    await applyPermissionPreset(page, 'smart')
    await expectSettingsChip(page, 'Ask When Needed')
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.KIMI_CODE, 'smart-delete', 'rm -r -f shortcut-dangerous-proof')] },
      { text: 'Smart kept the file.' },
    )
    await sendMessage(page, modelScript.prompt('Try the scripted delete under Smart permissions.'))
    await modelScript.waitForSteps(1)
    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('shortcut-dangerous-proof')
    await page.getByTestId('control-deny-btn').filter({ visible: true }).click()
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    expect(existsSync(directory)).toBe(true)

    await applyPermissionPreset(page, 'bypass')
    await expectSettingsChip(page, 'Never Ask')
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.KIMI_CODE, 'bypass-delete', 'rm -r -f shortcut-dangerous-proof')] },
      { text: 'Bypass removed the file.' },
    )
    await sendMessage(page, modelScript.prompt('Run the scripted delete under Bypass permissions.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expectNoControlBanner(page)
    expect(existsSync(directory)).toBe(false)

    // The kap-server holds the mode, and the worker reads it back on reload.
    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Never Ask')
  })
})
