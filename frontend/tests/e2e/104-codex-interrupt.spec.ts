import { codexTest, expect } from './codex-fixtures'
import { expectNoRegistryRows, waitForRegistryRow } from './helpers/subagentRegistry'
import { messageBubbles, sendMessage } from './helpers/ui'

codexTest.describe('codex interrupt', () => {
  codexTest('sends a prompt and interrupts mid-response', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace // fixture trigger
    // The long prompt keeps the interrupt window open. The assertion must fail
    // if the button never appears.
    await sendMessage(page, 'Write a very detailed essay about the history of computing, at least 5000 words across multiple chapters with subheadings.')

    // Click Interrupt. The button must appear within the test timeout.
    const interruptBtn = page.locator('[data-testid="interrupt-button"]')
    await expect(interruptBtn).toBeVisible()
    await interruptBtn.click()

    // After interrupt, the thinking indicator must clear, and at least
    // one user + partial-response bubble must be present.
    await expect(page.locator('[data-testid="thinking-indicator"]')).not.toBeVisible()
    // And the button goes with it. The press publishes the stop, so neither
    // survives the provider's round trip, and there is nothing left to press.
    await expect(interruptBtn).not.toBeVisible()
    const bubbles = messageBubbles(page)
    expect(await bubbles.count()).toBeGreaterThan(1)
  })

  codexTest('stops loading after root interruption while a subagent remains active', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace
    await expectNoRegistryRows(page)

    await sendMessage(page, [
      'Use spawn_agent exactly once with task_name "interrupt_probe_child".',
      'Tell the child to inspect every Go file under backend/internal/worker/agent and prepare a detailed report.',
      'Use wait_agent until the child finishes.',
    ].join(' '))

    const row = await waitForRegistryRow(page)
    await expect(row).toHaveAttribute('data-status', 'running')

    const interruptBtn = page.locator('[data-testid="interrupt-button"]:visible')
    await expect(interruptBtn).toBeVisible()
    await interruptBtn.click()
    await expect(interruptBtn).toHaveText('Interrupting...')

    await expect(page.locator('[data-testid="result-divider"]:visible').filter({ hasText: /^Turn interrupted$/ })).toBeVisible()
    await expect(interruptBtn).toBeEnabled()
    await expect(interruptBtn).toHaveText('Interrupt')
    await expect(row).toHaveAttribute('data-status', 'running')
  })
})
