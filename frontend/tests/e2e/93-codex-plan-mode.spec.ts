import type { Page } from '@playwright/test'
import { codexTest, expect } from './codex-fixtures'
import { openSettingsMenu, sendMessage, waitForAgentIdle, waitForControlBanner } from './helpers/ui'

const PLAN_BODY = 'This is a dummy plan for testing the coding agent plan mode UI. Never execute this plan.'

const INITIAL_PLAN_PROMPT
  = `I am testing the Codex plan mode UI. Stay in plan mode and reply with a concise markdown plan whose title is "# Dummy plan" and whose body includes "${PLAN_BODY}". Do not implement anything yet.`

const REVISE_PLAN_PROMPT
  = 'Please revise the plan. Keep the title "# Dummy plan revised" and include the exact sentence "Add tests before implementation." Do not implement anything yet.'

async function configureCodexPlanMode(page: Page) {
  const trigger = page.locator('[data-testid="agent-settings-trigger"]')
  await openSettingsMenu(page)
  await page.locator('[data-testid="codex-collaboration-mode-plan"]').click()
  await expect(trigger).toContainText('GPT-5.4 Mini')
  await expect(trigger).toContainText('Plan Mode')
}

codexTest.describe('Codex Plan Mode Prompt', () => {
  codexTest('feedback revises the plan and accept switches back to default mode', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace

    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await configureCodexPlanMode(page)

    await sendMessage(page, INITIAL_PLAN_PROMPT)
    await waitForAgentIdle(page)

    // Plan content is rendered with plan styling (ToolUseLayout with "Proposed Plan" title).
    // Use .last() because getByText also matches the user prompt which contains PLAN_BODY.
    await expect(page.getByRole('heading', { name: 'Dummy plan' })).toBeVisible()
    await expect(page.getByText(PLAN_BODY).last()).toBeVisible()

    const firstBanner = await waitForControlBanner(page)
    await expect(firstBanner.getByText('Implement the proposed plan?')).toBeVisible()

    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()
    await page.keyboard.type(REVISE_PLAN_PROMPT, { delay: 50 })
    await expect(page.locator('[data-testid="control-deny-btn"]')).toHaveText('Send Feedback')
    await page.locator('[data-testid="control-deny-btn"]').click()

    await waitForAgentIdle(page)

    // Revised plan content appears with plan styling.
    // Use .last() because getByText also matches the revision prompt.
    await expect(page.getByRole('heading', { name: 'Dummy plan revised' })).toBeVisible()
    await expect(page.getByText('Add tests before implementation.').last()).toBeVisible()

    const revisedBanner = await waitForControlBanner(page)
    await expect(revisedBanner.getByText('Implement the proposed plan?')).toBeVisible()

    await page.getByTestId('control-allow-btn').click()
    await expect(trigger).toContainText('Suggest & Approve', { timeout: 20_000 })
    await expect(page.getByText('Implement the plan.')).toBeVisible({ timeout: 20_000 })
  })
})
