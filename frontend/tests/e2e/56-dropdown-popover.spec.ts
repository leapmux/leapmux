import { expect, test } from './fixtures'
import { openAgentViaUI } from './helpers'

test.describe('DropdownMenu Popover – Focus and Positioning', () => {
  /**
   * Problem 1: Focus stealing on popover close.
   *
   * When the session-id popover (ContextUsageGrid trigger) is open and
   * the user clicks the MarkdownEditor text input area, the editor gains
   * focus momentarily but then loses it when the popover light-dismisses.
   * The browser's popover light-dismiss restores focus to the element
   * that was focused before the popover opened (the trigger button),
   * stealing focus from the editor.
   */
  test('clicking editor while popover is open should keep editor focused', async ({ page, authenticatedWorkspace }) => {
    // Ensure an agent tab is open
    await openAgentViaUI(page)

    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Send a message so the agent session starts and context info appears
    await editor.click()
    await page.keyboard.type('What is 1+1? Reply with just the number, nothing else.')
    await page.keyboard.press('Enter')
    await page.waitForFunction(() => {
      const body = document.body.textContent || ''
      return body.includes('2') && !body.includes('Send a message to start')
    }, {}, { timeout: 60_000 })

    // Wait for the ContextUsageGrid trigger to appear
    const infoTrigger = page.locator('[data-testid="session-id-trigger"]')
    const contextGrid = infoTrigger.locator('svg[viewBox="0 0 11 11"]')
    await expect(contextGrid).toBeVisible({ timeout: 60_000 })

    // Open the popover by clicking the trigger
    await infoTrigger.click()
    const popover = page.locator('[data-testid="session-id-popover"]')
    await expect(popover).toBeVisible()

    // Now click the editor text input area — this should light-dismiss the
    // popover and leave focus in the editor.
    // The popover may be positioned above the trigger (data-flipped), so
    // click near the top-right corner of the editor to avoid the popover.
    const editorBox = await editor.boundingBox()
    const popoverBox = await popover.boundingBox()
    expect(editorBox).not.toBeNull()
    expect(popoverBox).not.toBeNull()

    // Click at a point inside the editor but outside the popover
    let clickX = editorBox!.x + editorBox!.width - 20
    let clickY = editorBox!.y + 10
    // Make sure click point is outside the popover bounding box
    if (
      popoverBox
      && clickX >= popoverBox.x && clickX <= popoverBox.x + popoverBox.width
      && clickY >= popoverBox.y && clickY <= popoverBox.y + popoverBox.height
    ) {
      // Try top-left corner instead
      clickX = editorBox!.x + 20
      clickY = editorBox!.y + 10
    }
    await page.mouse.click(clickX, clickY)

    // Wait for the popover to close via light-dismiss
    await expect(popover).not.toBeVisible()

    // Give the browser a moment to settle focus.
    await page.waitForTimeout(200)

    // The editor should retain focus after the popover closes.
    const editorHasFocus = await page.evaluate(() => {
      const proseMirror = document.querySelector('[data-testid="chat-editor"] .ProseMirror')
      if (!proseMirror)
        return false
      return proseMirror.contains(document.activeElement) || proseMirror === document.activeElement
    })
    expect(editorHasFocus).toBe(true)
  })

  /**
   * Problem 2: Popover repositions when selecting text by dragging.
   *
   * When the session-id popover is open and the user drags to select text
   * inside the popover content, the popover suddenly changes position.
   * This happens because the drag/selection causes scroll events that
   * trigger the reposition logic.
   */
  test('selecting text inside popover by dragging should not reposition it', async ({ page, authenticatedWorkspace }) => {
    // Ensure an agent tab is open
    await openAgentViaUI(page)

    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Send a message so the agent session starts and context info appears
    await editor.click()
    await page.keyboard.type('What is 1+1? Reply with just the number, nothing else.')
    await page.keyboard.press('Enter')
    await page.waitForFunction(() => {
      const body = document.body.textContent || ''
      return body.includes('2') && !body.includes('Send a message to start')
    }, {}, { timeout: 60_000 })

    // Wait for the ContextUsageGrid trigger to appear and stabilize.
    // After the agent responds, several async events may arrive (session ID,
    // context info, git status) that cause the trigger to re-render.
    // Wait for the session ID to be set in the popover content as a signal
    // that all status updates have been applied.
    const infoTrigger = page.locator('[data-testid="session-id-trigger"]')
    const contextGrid = infoTrigger.locator('svg[viewBox="0 0 11 11"]')
    await expect(contextGrid).toBeVisible({ timeout: 60_000 })

    // Wait for the session-id-trigger to be stable (no re-renders) by
    // checking that it stays visible for a brief period.
    await page.waitForTimeout(1000)
    await expect(infoTrigger).toBeVisible()

    // Open the popover
    await infoTrigger.click()
    const popover = page.locator('[data-testid="session-id-popover"]')
    await expect(popover).toBeVisible()

    // Wait for initial positioning to stabilize
    await page.waitForTimeout(300)

    // Record the popover's initial position
    const initialPosition = await popover.boundingBox()
    expect(initialPosition).not.toBeNull()

    // Find a text element inside the popover to drag-select.
    // The popover has info rows with labels like "Session ID", "Context", etc.
    const popoverText = popover.locator('span, div').filter({ hasText: /.+/ }).first()
    await expect(popoverText).toBeVisible()
    const textBox = await popoverText.boundingBox()
    expect(textBox).not.toBeNull()

    // Perform a drag operation inside the popover to select text.
    // Start from left side of the text element and drag to the right.
    await page.mouse.move(textBox!.x + 2, textBox!.y + textBox!.height / 2)
    await page.mouse.down()
    // Drag slowly across the text
    for (let i = 0; i < 5; i++) {
      await page.mouse.move(
        textBox!.x + (textBox!.width * (i + 1)) / 5,
        textBox!.y + textBox!.height / 2,
      )
      await page.waitForTimeout(50)
    }
    await page.mouse.up()

    // Check that the popover position has NOT changed
    const finalPosition = await popover.boundingBox()
    expect(finalPosition).not.toBeNull()
    expect(finalPosition!.x).toBeCloseTo(initialPosition!.x, 0)
    expect(finalPosition!.y).toBeCloseTo(initialPosition!.y, 0)
  })
})
