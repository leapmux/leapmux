import { expect, test } from './fixtures'
import { expectAssistantAnswer, openSettingsMenu, waitForSettingsIdle } from './helpers/ui'

// The e2e account can't bill Sonnet's 1M-context tier, so this suite uses
// Opus[1m] instead. The underlying coverage — bracketed model IDs and the
// settings-change notification — is the same.
const MODEL_CHANGE_PATTERN = /Model.*Sonnet.*Opus.*1M/

test.describe('1m-context model', () => {
  test('switch to opus[1m] and exchange messages', async ({ authenticatedWorkspace, page }) => {
    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    // Default model should be Sonnet
    await expect(trigger).toContainText('Sonnet')

    // Switch to Opus[1m]
    await openSettingsMenu(page)
    await page.locator('[data-testid="model-opus\\[1m\\]"]').click()
    await expect(trigger).toContainText('Opus (1M context)')

    // Verify the settings change notification appears in chat
    await expect(page.getByText(MODEL_CHANGE_PATTERN)).toBeVisible()

    // Wait for agent restart to complete
    await waitForSettingsIdle(page)

    // Send a message and verify the agent responds
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()
    await page.keyboard.type('What is 5+3? Reply with just the number, nothing else.')
    await page.keyboard.press('Meta+Enter')

    // Scan all bubbles for the answer rather than .last(): the per-turn "Took Ns"
    // meta bubble also carries data-role="agent" and can be last, racing the
    // answer bubble.
    await expectAssistantAnswer(page, { answer: /\b8\b/ })

    // Send a follow-up to confirm the agent session is stable
    await editor.click()
    await page.keyboard.type('What is 10-4? Reply with just the number, nothing else.')
    await page.keyboard.press('Meta+Enter')

    await expectAssistantAnswer(page, { answer: /\b6\b/ })

    // Verify the model is still shown as Opus[1m] after exchanging messages
    await expect(trigger).toContainText('Opus (1M context)')
  })

  // Regression for the account-default round-trip: switching from a concrete
  // model back to "Default (recommended)" must resolve to a concrete model (with
  // its effort menu), never freeze the trigger on the "default" sentinel label
  // with no effort mark. The CLI resolves "default" against the resumed session,
  // which keeps its concrete model (Opus here) rather than re-deriving the
  // account default -- so this asserts the model-agnostic property (not-sentinel
  // + effort group present), not a specific resolved id. No messages are
  // exchanged -- the bug is purely in the settings round-trip -- so it stays
  // cheap and off the billable path.
  test('switching to Default resolves to a concrete model with its effort menu', async ({ authenticatedWorkspace, page }) => {
    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    // A fresh tab starts on the account default, which resolves to Sonnet.
    await expect(trigger).toContainText('Sonnet')

    // Move off the default onto a concrete non-default model.
    await openSettingsMenu(page)
    await page.locator('[data-testid="model-opus\\[1m\\]"]').click()
    await expect(trigger).toContainText('Opus (1M context)')
    await waitForSettingsIdle(page)

    // Switch back to "Default (recommended)". The worker relaunches without
    // --model and the CLI resolves the sentinel to the session's concrete model.
    await openSettingsMenu(page)
    await page.locator('[data-testid="model-default"]').click()
    await waitForSettingsIdle(page)

    // The trigger settles on a concrete model -- NEVER stuck on the sentinel's
    // "Default (recommended)" label (the reported bug).
    await expect(trigger).not.toContainText('Default (recommended)')

    // ...and the effort group returns with it -- the sentinel carries none, which
    // is exactly why the broken state dropped the effort mark. Both the account
    // default (Sonnet) and a resumed session's model (Opus) offer the "high"
    // tier, so assert on it regardless of which the CLI resolved.
    await openSettingsMenu(page)
    await expect(page.locator('[data-testid="effort-high"]')).toBeVisible()
  })
})
