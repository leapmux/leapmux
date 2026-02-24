import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'

/** Send a message via the ProseMirror editor. */
async function sendMessage(page: Page, text: string) {
  const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
  await expect(editor).toBeVisible()
  await editor.click()
  await page.keyboard.type(text, { delay: 100 })
  await page.keyboard.press('Meta+Enter')
  await expect(editor).toHaveText('')
}

/** Wait for the control request banner to appear and return a scoped locator. */
async function waitForControlBanner(page: Page) {
  const banner = page.locator('[data-testid="control-banner"]')
  await expect(banner).toBeVisible()
  return banner
}

/**
 * Click an option radio/checkbox inside the control banner.
 * Kobalte's RadioGroup.Item outer div (which carries data-testid) has no
 * onClick handler.  Selection is triggered either through ItemControl
 * (visual circle) or ItemLabel (a real `<label for="...">` element).
 * We click the `<label>` which is the largest hit area and triggers
 * native label→input association, firing Kobalte's onChange.
 */
async function clickOption(page: Page, label: string) {
  const option = page.locator(`[data-testid="question-option-${label}"]`)
  await expect(option).toBeVisible()
  await option.click()
}

test.describe('Control Request - AskUserQuestion', () => {
  test('single question - select an option and submit', async ({ page, authenticatedWorkspace }) => {
    // Send a message that triggers AskUserQuestion
    await sendMessage(
      page,
      'Use AskUserQuestion and tell me what I answered: {"questions":[{"question":"Pick a color","header":"Color","multiSelect":false,"options":[{"label":"Red"},{"label":"Blue"},{"label":"Green"}]}]}',
    )

    // Wait for the control banner
    const banner = await waitForControlBanner(page)

    // Verify question text and options (scoped to banner to avoid matching chat messages)
    await expect(banner.getByText('Pick a color')).toBeVisible()
    await expect(page.locator('[data-testid="question-option-Red"]')).toBeVisible()
    await expect(page.locator('[data-testid="question-option-Blue"]')).toBeVisible()
    await expect(page.locator('[data-testid="question-option-Green"]')).toBeVisible()

    // Click "Blue" option
    await clickOption(page, 'Blue')

    // Verify Stop and Submit buttons are visible
    await expect(page.locator('[data-testid="control-stop-btn"]')).toBeVisible()
    await expect(page.locator('[data-testid="control-submit-btn"]')).toBeVisible()

    // Wait for Submit to become enabled, then click it
    const submitBtn = page.locator('[data-testid="control-submit-btn"]')
    await expect(submitBtn).toBeEnabled()
    await submitBtn.click()

    // Wait for assistant response containing "Blue"
    await page.waitForFunction(() => {
      const body = document.body.textContent || ''
      return body.includes('Blue')
    })
  })

  test('multi-question - pagination with option selection', async ({ page, authenticatedWorkspace }) => {
    // Send a message with 2 questions
    await sendMessage(
      page,
      'Use AskUserQuestion and tell me what I answered: {"questions":[{"question":"Pick a color","header":"Color","multiSelect":false,"options":[{"label":"Red"},{"label":"Blue"}]},{"question":"Pick a size","header":"Size","multiSelect":false,"options":[{"label":"Small"},{"label":"Large"}]}]}',
    )

    const banner = await waitForControlBanner(page)

    // Verify only question 1 is shown (scoped to banner)
    await expect(banner.getByText('Pick a color')).toBeVisible()
    await expect(banner.getByText('Pick a size')).not.toBeVisible()

    // Verify pagination shows 2 page items
    const pagination = page.locator('[data-testid="control-pagination"]')
    await expect(pagination).toBeVisible()
    const pageButtons = pagination.locator('button')
    await expect(pageButtons).toHaveCount(2)

    // Answer question 1 by clicking "Red" -- should auto-advance to page 2
    await clickOption(page, 'Red')

    // Verify question 2 is now shown (scoped to banner)
    await expect(banner.getByText('Pick a size')).toBeVisible()
    await expect(banner.getByText('Pick a color')).not.toBeVisible()

    // Answer question 2 by clicking "Large"
    await clickOption(page, 'Large')

    // Wait for Submit to become enabled, then click it
    const submitBtn = page.locator('[data-testid="control-submit-btn"]')
    await expect(submitBtn).toBeEnabled()
    await submitBtn.click()

    // Wait for assistant response containing both answers
    await page.waitForFunction(() => {
      const body = document.body.textContent || ''
      return body.includes('Red') && body.includes('Large')
    })
  })

  test('multi-question - option click auto-advances to next page', async ({ page, authenticatedWorkspace }) => {
    await sendMessage(
      page,
      'Use AskUserQuestion and tell me what I answered: {"questions":[{"question":"Pick a color","header":"Color","multiSelect":false,"options":[{"label":"Red"},{"label":"Blue"}]},{"question":"Pick a size","header":"Size","multiSelect":false,"options":[{"label":"Small"},{"label":"Large"}]}]}',
    )

    const banner = await waitForControlBanner(page)

    // Verify page 1 shown (scoped to banner)
    await expect(banner.getByText('Pick a color')).toBeVisible()

    // Click "Red" -- should auto-advance
    await clickOption(page, 'Red')

    // Verify auto-advanced to page 2 (scoped to banner)
    await expect(banner.getByText('Pick a size')).toBeVisible()

    // Click "Large" on page 2 -- should stay on page 2 (last page)
    await clickOption(page, 'Large')
    await expect(banner.getByText('Pick a size')).toBeVisible()

    // Wait for Submit to become enabled, then click it
    const submitBtn = page.locator('[data-testid="control-submit-btn"]')
    await expect(submitBtn).toBeEnabled()
    await submitBtn.click()

    // Wait for response
    await page.waitForFunction(() => {
      const body = document.body.textContent || ''
      return body.includes('Red') && body.includes('Large')
    })
  })

  test('YOLO button fills unanswered questions', async ({ page, authenticatedWorkspace }) => {
    await sendMessage(
      page,
      'Use AskUserQuestion and tell me what I answered: {"questions":[{"question":"Pick a color","header":"Color","multiSelect":false,"options":[{"label":"Red"},{"label":"Blue"}]},{"question":"Pick a size","header":"Size","multiSelect":false,"options":[{"label":"Small"},{"label":"Large"}]}]}',
    )

    await waitForControlBanner(page)

    // Answer only question 1
    await clickOption(page, 'Red')

    // Go back to page 1 to verify YOLO is visible while Q2 is unanswered
    // (auto-advanced to page 2, but we check YOLO on any page)
    await expect(page.locator('[data-testid="control-yolo-btn"]')).toBeVisible()

    // Click YOLO
    await page.locator('[data-testid="control-yolo-btn"]').click()

    // Wait for assistant response (YOLO auto-submits after filling)
    await page.waitForFunction(() => {
      const body = document.body.textContent || ''
      return body.includes('Red') && body.includes('recommended')
    })
  })

  test('Stop button rejects the request', async ({ page, authenticatedWorkspace }) => {
    await sendMessage(
      page,
      'Use AskUserQuestion and tell me what I answered: {"questions":[{"question":"Pick a color","header":"Color","multiSelect":false,"options":[{"label":"Red"},{"label":"Blue"}]}]}',
    )

    await waitForControlBanner(page)

    // Click Stop
    await page.locator('[data-testid="control-stop-btn"]').click()

    // Verify control banner disappears
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()
  })
})
