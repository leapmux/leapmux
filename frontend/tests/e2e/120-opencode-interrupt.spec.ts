import { sendMessage } from './helpers/ui'
import { expect, OPENCODE_E2E_SKIP_REASON, opencodeTest } from './opencode-fixtures'

opencodeTest.skip(!!OPENCODE_E2E_SKIP_REASON, OPENCODE_E2E_SKIP_REASON || '')

opencodeTest.describe('OpenCode Interrupt', () => {
  opencodeTest('interrupt button appears during processing', async ({ authenticatedOpencodeWorkspace, page, modelScript }) => {
    void authenticatedOpencodeWorkspace // fixture trigger

    // A held answer is what keeps the agent busy. A long PROMPT no longer does:
    // the mock endpoint answers in milliseconds whatever its length.
    await modelScript.queue({ text: 'An essay.', delayMs: 60_000 })
    modelScript.allowUnconsumed('the interrupt ends the turn before the held answer arrives')
    await sendMessage(page, modelScript.prompt('Write a very long essay about the history of computing.'))
    await modelScript.waitForSteps()

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
