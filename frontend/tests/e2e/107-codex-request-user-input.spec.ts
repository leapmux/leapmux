import { codexTest, expect } from './codex-fixtures'
import { isMaybeVisible, messageContents, openSettingsMenu, sendMessage, waitForAgentIdle, waitForSettingsIdle } from './helpers/ui'

codexTest.describe('codex approval UI', () => {
  codexTest('approval flow works with on-request policy', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace

    // Switch to on-request approval policy so approval prompts appear.
    await openSettingsMenu(page, 'permissionMode')
    const onRequestRadio = page.locator('[data-testid="permissionMode-on-request"]')
    await expect(onRequestRadio).toBeVisible()
    await onRequestRadio.click()
    await waitForSettingsIdle(page)

    // Close the menu by clicking elsewhere.
    await page.locator('[data-testid="composer-editor"] .ProseMirror').click()

    // Send a command that will trigger an approval request.
    // Use rm which should always require approval in on-request mode.
    await sendMessage(page, 'Run this exact command: rm -rf /tmp/codex-approval-test-dir-nonexistent')

    // Wait for the control banner to appear.
    const banner = page.locator('[data-testid="control-banner"]')
    await expect(banner).toBeVisible()

    // The allow-choice pills expose Codex's own decisions without the former
    // Remember switch. The group appears only when the CLI offers `accept` plus
    // a second allow decision, and which second one it offers is the CLI's
    // choice -- so the pills are checked ONLY when the group renders. The
    // approval round-trip below is what this spec exists for, and it must fail
    // on its own terms rather than on a missing radio.
    const allowChoices = page.getByRole('radiogroup', { name: 'Allow as' })
    if (await isMaybeVisible(allowChoices)) {
      const once = allowChoices.getByRole('radio', { name: 'Once' })
      await expect(once).toBeChecked()
      const remembering = allowChoices.getByRole('radio').nth(1)
      await remembering.click()
      await expect(remembering).toBeChecked()
      // Return to the one-turn decision before approval. A browser test must not
      // persist a real Codex command or host rule in the developer's account state.
      await once.click()
      await expect(once).toBeChecked()
    }

    const allowBtn = page.locator('[data-testid="control-allow-btn"]')
    await expect(allowBtn).toBeVisible()
    await allowBtn.click()

    // Wait for the agent to finish and verify the command ran.
    await waitForAgentIdle(page, 120_000)
    const chatArea = messageContents(page)
    await expect.poll(async () => (await chatArea.allTextContents()).join(' '))
      .toContain('codex-approval-test-dir-nonexistent')
  })
})
