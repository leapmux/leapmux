import { expect, test } from './fixtures'
import { enterAndExitPlanMode } from './helpers/plan-mode'

const MONOSPACE_FONT_RE = /HackNerdFont|Menlo|Monaco|Courier New|monospace/

test.describe('Markdown Editor', () => {
  test('should grow editor beyond old 120px limit', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Default mode is Cmd+Enter-to-send, so Enter creates newlines
    await editor.click()

    // Type many lines to exceed the old 120px limit
    for (let i = 0; i < 15; i++) {
      await page.keyboard.type(`Line ${i + 1}`, { delay: 100 })
      await page.keyboard.press('Enter')
    }

    // The editor wrapper should have grown (capped at 75% of container)
    const wrapper = page.locator('[data-testid="chat-editor"]')
    const height = await wrapper.evaluate(el => el.getBoundingClientRect().height)
    expect(height).toBeGreaterThan(60)
  })

  test('should use --mono-font-family CSS variable for code elements', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    await editor.click()
    await page.keyboard.type('some code')

    // Select all text, then apply inline code. The formatting toolbar is gone,
    // so this drives the markdown input rule that replaced it.
    await page.keyboard.press('Meta+a')
    await page.keyboard.type('`some code`')

    // Check that the code element's computed font-family includes the fallback
    const fontFamily = await editor.locator('code').evaluate(
      el => window.getComputedStyle(el).fontFamily,
    )
    // Should contain at least one of the expected monospace fonts
    expect(fontFamily).toMatch(MONOSPACE_FONT_RE)
  })
})

test.describe('Code Language Label', () => {
  test('clicking language label opens popover and selecting language updates it', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Create a code block. The ``` input rule fires on the third backtick.
    await page.keyboard.type('```')

    // Language label should show "plaintext" by default
    const langLabel = editor.locator('.code-lang-label')
    await expect(langLabel).toHaveText('plaintext')

    // Click the language label
    await langLabel.click()

    // The language input should appear
    const langInput = page.locator('[data-testid="code-lang-filter"]')
    await expect(langInput).toBeVisible()

    // The popover must be anchored to the label (just below it, or flipped just
    // above it near the bottom of the viewport) -- NOT re-centered in the viewport.
    // Regression guard for the missing `margin: 0` reset, which let the UA
    // `margin: auto` recenter the popover and clip its bottom / leave a dead area.
    const popover = page.locator('[data-testid="code-lang-popover"]')
    await expect(popover).toBeVisible()
    const labelBox = (await langLabel.boundingBox())!
    const popBox = (await popover.boundingBox())!
    const viewport = page.viewportSize()!
    const anchoredBelow = Math.abs(popBox.y - (labelBox.y + labelBox.height)) <= 24
    const anchoredAbove = Math.abs((popBox.y + popBox.height) - labelBox.y) <= 24
    expect(anchoredBelow || anchoredAbove, 'popover anchored to the language label').toBe(true)
    // ...and fully on-screen (no clipped bottom).
    expect(popBox.x).toBeGreaterThanOrEqual(0)
    expect(popBox.y).toBeGreaterThanOrEqual(0)
    expect(popBox.x + popBox.width).toBeLessThanOrEqual(viewport.width + 1)
    expect(popBox.y + popBox.height).toBeLessThanOrEqual(viewport.height + 1)
    // The language list rendered (not an empty popover).
    await expect(page.locator('[data-testid="code-lang-plaintext"]')).toBeVisible()

    // Type and select a language
    await langInput.fill('python')
    await page.keyboard.press('Enter')

    // The label should now show "python"
    await expect(langLabel).toHaveText('python')
    // ...and the popover must fully close (no lingering empty popover).
    await expect(popover).toBeHidden()
  })

  test('re-clicking the language label closes the popover instead of reopening it', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()
    await page.keyboard.type('```')

    const langLabel = editor.locator('.code-lang-label')
    const popover = page.locator('[data-testid="code-lang-popover"]')
    const langInput = page.locator('[data-testid="code-lang-filter"]')

    // First click opens it.
    await langLabel.click()
    await expect(langInput).toBeVisible()

    // Re-clicking the label toggles it closed -- it must NOT reopen (the
    // pointerdown-captured open state prevents the click from re-opening after the
    // popover's light-dismiss closes it).
    await langLabel.click()
    await expect(popover).toBeHidden()
  })
})

test.describe('Send Feedback Button Labels', () => {
  test('ExitPlanMode banner shows Reject when editor is empty, Send Feedback when typing', async ({ page, authenticatedWorkspace }) => {
    // Enter plan mode, write a dummy plan, and exit
    const banner = await enterAndExitPlanMode(page)
    await expect(banner.getByText('Plan Ready for Review')).toBeVisible()

    const rejectBtn = page.locator('[data-testid="plan-reject-btn"]')

    // With empty editor, button should say "Reject"
    await expect(rejectBtn).toHaveText('Reject')

    // Type feedback into the editor — button should switch to "Send Feedback"
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await editor.click()
    await page.keyboard.type('Please reconsider this approach')
    await expect(rejectBtn).toHaveText('Send Feedback')
  })
})

test.describe('Markdown Editor links', () => {
  test('a link URL can be corrected after it is created', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Typed markdown is one of the two ways to create a link; Mod-K over a
    // selection is the other (covered below).
    await page.keyboard.type('[docs](https://old.test)')
    const link = editor.locator('a')
    await expect(link).toHaveAttribute('href', 'https://old.test')

    // Clicking the link opens the edit popover seeded with the current URL.
    await link.click()
    const url = page.locator('[data-testid="link-url-input"]')
    await expect(url).toBeVisible()
    await expect(url).toHaveValue('https://old.test')

    // Correcting the URL rewrites the mark rather than adding a second one.
    // Without this the href is unreachable: editing the visible text in place,
    // or deleting and retyping it, both keep the original URL because the link
    // mark is inclusive.
    await url.fill('https://new.test')
    await page.locator('[data-testid="link-url-submit"]').click()
    await expect(editor.locator('a')).toHaveAttribute('href', 'https://new.test')
    await expect(editor.locator('a')).toHaveCount(1)
    // The edit is finished, so the panel goes away rather than sitting over the
    // text it just changed.
    await expect(page.locator('[data-testid="link-popover"]')).toBeHidden()
  })

  test('Enter in the URL field applies and dismisses the popover', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    await page.keyboard.type('[docs](https://old.test)')
    await editor.locator('a').click()
    const url = page.locator('[data-testid="link-url-input"]')
    await expect(url).toBeVisible()

    // Enter submits the form -- the same path the Save button takes.
    await url.fill('https://typed.test')
    await url.press('Enter')

    await expect(editor.locator('a')).toHaveAttribute('href', 'https://typed.test')
    await expect(page.locator('[data-testid="link-popover"]')).toBeHidden()
  })

  test('mod+K links the selected text', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Select-all rather than a run of Shift+ArrowLeft: the arrow presses race
    // ProseMirror's input handling and drop some, which silently narrows the
    // selection. WHICH range a partial selection resolves to is pinned exactly
    // by linkPlugin.test.ts (`linkShortcutTarget`); this spec covers the wiring.
    await page.keyboard.type('design doc')
    await page.keyboard.press('ControlOrMeta+a')

    // The popover opens EMPTY: a selection is a request to link that text, not
    // to edit an existing URL.
    await page.keyboard.press('ControlOrMeta+k')
    const url = page.locator('[data-testid="link-url-input"]')
    await expect(url).toBeVisible()
    await expect(url).toHaveValue('')

    await url.fill('https://x.test')
    await page.locator('[data-testid="link-url-submit"]').click()

    const link = editor.locator('a')
    await expect(link).toHaveCount(1)
    await expect(link).toHaveAttribute('href', 'https://x.test')
    await expect(link).toHaveText('design doc')
    await expect(page.locator('[data-testid="link-popover"]')).toBeHidden()
  })

  test('mod+K on a caret in a link edits that link, and over it overrides it', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    await page.keyboard.type('[docs](https://old.test)')
    await expect(editor.locator('a')).toHaveAttribute('href', 'https://old.test')

    // A bare CARET inside the link: the popover opens on its current URL.
    await editor.locator('a').click()
    await page.locator('[data-testid="link-url-input"]').press('Escape')
    await editor.locator('a').click()
    await page.keyboard.press('ControlOrMeta+k')
    const url = page.locator('[data-testid="link-url-input"]')
    await expect(url).toBeVisible()
    await expect(url).toHaveValue('https://old.test')
    await url.press('Escape')

    // A SELECTION spanning the link: the popover opens empty, and applying
    // OVERRIDES the link rather than leaving the text carrying two.
    await editor.click()
    await page.keyboard.press('ControlOrMeta+a')
    await page.keyboard.press('ControlOrMeta+k')
    await expect(url).toBeVisible()
    await expect(url).toHaveValue('')

    await url.fill('https://new.test')
    await page.locator('[data-testid="link-url-submit"]').click()
    await expect(editor.locator('a')).toHaveCount(1)
    await expect(editor.locator('a')).toHaveAttribute('href', 'https://new.test')
    await expect(page.locator('[data-testid="link-popover"]')).toBeHidden()
  })

  test('a link can be unmade, keeping its text', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    await page.keyboard.type('[docs](https://old.test)')
    await expect(editor.locator('a')).toHaveCount(1)

    await editor.locator('a').click()
    await expect(page.locator('[data-testid="link-url-input"]')).toBeVisible()
    await page.locator('[data-testid="link-url-remove"]').click()

    await expect(editor.locator('a')).toHaveCount(0)
    await expect(editor).toContainText('docs')
  })

  test('saving dismisses the popover, and it reopens cleanly afterwards', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()
    await page.keyboard.type('[docs](https://old.test)')

    const url = page.locator('[data-testid="link-url-input"]')
    await editor.locator('a').click()
    await expect(url).toBeVisible()
    await url.fill('https://new.test')
    await page.locator('[data-testid="link-url-submit"]').click()
    await expect(editor.locator('a')).toHaveAttribute('href', 'https://new.test')
    await expect(page.locator('[data-testid="link-popover"]')).toBeHidden()

    // Reopening is a SEPARATE gesture, so it is outside Oat's 150ms close
    // transition -- the window in which `showPopover()` would re-enter an
    // element the browser is still removing from the top layer and bring the
    // panel back BELOW the page, where the transcript swallows its buttons.
    // This asserts the reopened panel is usable, not merely present.
    await editor.locator('a').click()
    await expect(url).toBeVisible()
    await expect(url).toHaveValue('https://new.test')

    await page.locator('[data-testid="link-url-remove"]').click()
    await expect(editor.locator('a')).toHaveCount(0)
    await expect(editor).toContainText('docs')
  })

  test('the link popover fits its own box, with no horizontal overflow', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()
    await page.keyboard.type('[docs](https://a-fairly-long-example-url.test/some/path)')

    await editor.locator('a').click()
    await expect(page.locator('[data-testid="link-url-input"]')).toBeVisible()

    // The row used to be wider than the card that held it, which grew a
    // horizontal scrollbar and pushed the remove button out of view.
    const overflow = await page.evaluate(() => {
      const pop = document.querySelector('[data-testid="link-popover"]') as HTMLElement
      return { scrollWidth: pop.scrollWidth, clientWidth: pop.clientWidth }
    })
    expect(overflow.scrollWidth).toBeLessThanOrEqual(overflow.clientWidth)
    await expect(page.locator('[data-testid="link-url-remove"]')).toBeVisible()
  })
})
