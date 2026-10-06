import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { bashToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, expectNoControlBanner, expectPermissionShortcuts, expectSettingsChip, sendMessage, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'

kimiTest.describe('applies Kimi Code permission presets', () => {
  kimiTest('starts on Always Ask and offers both shortcuts', async ({ authenticatedKimiWorkspace, page }) => {
    void authenticatedKimiWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Always Ask')
    await expectPermissionShortcuts(page, { smart: 'offered', bypass: 'offered' })
  })

  kimiTest('the smart shortcut asks before a risky command and bypass runs it', async ({ authenticatedKimiWorkspace, native }) => {
    const { page, modelScript } = native
    await waitForSettingsHydrated(page)
    // Kimi treats removal of one file as routine. Forced recursive removal of
    // this test directory reaches the Smart permission check.
    const directory = join(authenticatedKimiWorkspace.workingDir, 'shortcut-dangerous-proof')
    mkdirSync(directory)
    writeFileSync(join(directory, 'keep.txt'), 'delete only in bypass\n')

    await applyPermissionPreset(page, 'smart')
    await expectSettingsChip(page, 'Ask When Needed')
    await exerciseNativePermissionDecision(native, {
      toolCall: bashToolCall(native.provider, 'smart-delete', 'rm -r -f shortcut-dangerous-proof'),
      decision: 'deny',
      beforeDecision: banner => expect(banner).toContainText('shortcut-dangerous-proof'),
      nativeProof: () => {
        expect(existsSync(directory)).toBe(true)
      },
    })

    await applyPermissionPreset(page, 'bypass')
    await expectSettingsChip(page, 'Never Ask')
    const bypass = await modelScript.queue(
      { toolCalls: [bashToolCall(native.provider, 'bypass-delete', 'rm -r -f shortcut-dangerous-proof')] },
      { text: 'Bypass removed the file.' },
    )
    await sendMessage(page, modelScript.prompt('Run the scripted delete under Bypass permissions.'))
    await modelScript.waitForSteps(bypass + 2)
    await waitForAgentIdle(page)
    await expectNoControlBanner(page)
    expect(existsSync(directory)).toBe(false)

    // The kap-server holds the mode, and the worker reads it back on reload.
    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Never Ask')
  })
})
