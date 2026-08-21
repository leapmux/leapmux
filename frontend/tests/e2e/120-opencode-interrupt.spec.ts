import { sendMessage } from './helpers/ui'
import { expect, OPENCODE_E2E_SKIP_REASON, opencodeTest } from './opencode-fixtures'

opencodeTest.skip(!!OPENCODE_E2E_SKIP_REASON, OPENCODE_E2E_SKIP_REASON || '')

opencodeTest.describe('OpenCode Interrupt', () => {
  opencodeTest('interrupt button appears during processing', async ({ authenticatedOpencodeWorkspace, page }) => {
    void authenticatedOpencodeWorkspace // fixture trigger

    // Send a prompt long enough that the agent must be visibly streaming
    // for several seconds — Interrupt must appear during that window.
    await sendMessage(page, 'Write a very long essay about the history of computing, covering all major milestones from the abacus to modern AI. Aim for at least 3000 words across multiple chapters.')

    // The Interrupt button must appear while the agent is processing.
    // If it never does (regression: button never wired up, or button stays
    // hidden), the assertion must fail rather than be swallowed.
    const interruptButton = page.locator('[data-testid="interrupt-button"]')
    await expect(interruptButton).toBeVisible()

    // Click the interrupt and confirm processing stops.
    //
    // The assertion below is unproven for OpenCode. It carried a dead test id
    // until the button locator above was repaired, so it never ran, and the
    // skip at the top of this file keeps it from running without an OpenCode
    // install. Codex fails the same assertion:
    // https://github.com/leapmux/leapmux/issues/401.
    await interruptButton.click()
    await expect(page.locator('[data-testid="thinking-indicator"]')).not.toBeVisible()
  })
})
