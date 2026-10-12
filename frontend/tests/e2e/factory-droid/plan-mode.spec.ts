import { expect } from '@playwright/test'
import { droidTest } from '../droid-fixtures'
import { assistantBubbles, sendMessage, toggleModeWithShortcut, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'
import { expectDroidNativeSettings } from './settingsUpdates'

droidTest.describe('Factory Droid Spec mode', () => {
  droidTest('Shift+Tab selects the native Spec mode and offers ExitSpecMode', async ({ askingDroidWorkspace, page, modelScript, leapmuxServer }) => {
    void askingDroidWorkspace
    await waitForSettingsHydrated(page)
    await toggleModeWithShortcut(page, 'Spec')
    await expectDroidNativeSettings({ page, leapmuxServer }, { interactionMode: 'spec', autonomyLevel: 'off' })

    const start = await modelScript.queue({ text: 'Spec mode is active.' })
    await sendMessage(page, modelScript.prompt('Reply while Spec mode is active.'))
    await modelScript.waitForSteps(start + 1)
    await waitForAgentIdle(page)
    expect(JSON.stringify((await modelScript.requestAt(start)).body)).toContain('"name":"ExitSpecMode"')
    await expect(assistantBubbles(page).filter({ hasText: 'Spec mode is active.' }).first()).toBeVisible()

    await toggleModeWithShortcut(page, 'Default')
  })
})
