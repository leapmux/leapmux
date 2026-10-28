import { expect } from '@playwright/test'
import { withCleanup } from '../helpers/cleanup'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { interruptButton, tabById } from '../helpers/ui'
import { museTest } from '../muse-fixtures'
import { MUSE_CHILD_GATE, openMuseChild } from './subagent-common.muse'

museTest('interrupts only the selected native child and keeps its parent usable', async ({ native }) => {
  const { page, modelScript } = native
  await withCleanup(async () => {
    const child = await openMuseChild(native, true)
    const interrupt = interruptButton(page)
    await expect(interrupt).toBeVisible()
    await interrupt.click()
    await modelScript.releaseGateIfHeld(MUSE_CHILD_GATE)
    await expect(child.row).toHaveAttribute('data-status', 'stopped')
    await expect(page.locator('[data-testid="thinking-indicator"]:visible')).toHaveCount(0)
    await expect(interrupt).toHaveCount(0)
    await child.finish()
    await tabById(page, child.parentId).click()
    await sendNativeAnswer(native, 'Continue the parent after cancelling its child.', 'The parent continues after its child stopped.')
    expect((await modelScript.status()).ruleMatches['the Muse child submits its own result'] ?? 0).toBe(0)
    await page.reload()
    await expect(child.row).toHaveAttribute('data-status', 'stopped')
  }, async () => { await modelScript.releaseGateIfHeld(MUSE_CHILD_GATE) })
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/518.
