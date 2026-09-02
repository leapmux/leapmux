import { CODE_BLOCK_TINT_PERCENT } from '../../src/styles/codePalette'
import { colorAlpha } from '../../src/test-support/color'
import { expect, test } from './fixtures'
import { enterAndExitPlanMode } from './helpers/plan-mode'
import { readAttached, resolvedColor, userBubbles } from './helpers/ui'

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

test.describe('Code block field', () => {
  /**
   * A fenced code block has to READ as a block on every surface the app puts a
   * message body on -- the panel behind the composer, an assistant band, and a
   * user message's accent bubble. One opaque colour cannot relate to all three,
   * so the field is a TRANSLUCENT step that composites onto whichever one hosts
   * it, and these cases assert the cascade delivers exactly that.
   *
   * The division of labour with `src/styles/codePalette.test.ts` is deliberate:
   * that suite measures the step against all thirty variants on all three hosts,
   * which no browser test can sweep. What only a browser settles is that the
   * rules actually resolve -- the field is translucent rather than flat, it is
   * the same declaration wherever a block lands, and it turns opaque for the one
   * case a tint cannot answer.
   */
  async function blockBackground(block: import('@playwright/test').Locator): Promise<string> {
    // Skip a detached match, because half these blocks live in a chat row: a row
    // that remounts between a resolve and a read leaves a DETACHED node, whose
    // computed style is empty rather than wrong. See readAttached.
    return readAttached(block, 'blockBackground', (matches) => {
      const el = matches.find(candidate => candidate.isConnected)
      return el ? getComputedStyle(el).backgroundColor : null
    })
  }

  test('paints the composer code block a field that composites on its host, in both polarities', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // The ``` input rule fires on the third backtick.
    await page.keyboard.type('```')
    const block = editor.locator('pre')
    await expect(block).toBeVisible()

    // The default theme mode is `system`, so emulating the OS preference flips
    // the app without opening a dialog over the editor under test.
    const fields = new Map<string, string>()
    for (const colorScheme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme })
      await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
      await expect
        .poll(() => blockBackground(block).then(colorAlpha), { message: `${colorScheme}: the field must composite on its host, not cover it` })
        .toBeCloseTo(CODE_BLOCK_TINT_PERCENT / 100, 4)
      fields.set(colorScheme, await blockBackground(block))

      // ...and the field is ALL that marks it. A fenced block carries no
      // outline, deliberately, so re-adding one is a visual decision rather than
      // something that slips back in with a shared `<pre>` rule.
      const border = await block.evaluate(el => getComputedStyle(el).borderTopWidth)
      expect(border, `${colorScheme}: a fenced block is unbordered`).toBe('0px')
    }
    // Each polarity steps from its OWN ink, rather than one of them keeping the
    // other's -- the failure a fallback rule that outranks the variant rules
    // produces.
    expect(fields.get('light')).not.toBe(fields.get('dark'))
  })

  test('paints a sent code block the same field as the one in the composer', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()
    await page.keyboard.type('```')
    await page.keyboard.type('const answer = 42')

    const composed = await blockBackground(editor.locator('pre'))

    await page.keyboard.press('Meta+Enter')
    await expect(editor).toHaveText('')

    // The transcript renders through a different stylesheet than the editor
    // (`markdownContent` vs the ProseMirror rules), and the two used to scope
    // the field differently: the transcript claimed only `pre.shiki`, so the
    // plain placeholder the synchronous render emits first was a differently
    // coloured block that swapped under the reader.
    const bubble = userBubbles(page).first()
    const sent = bubble.locator('pre')
    await expect(sent).toBeVisible()
    await expect
      .poll(() => blockBackground(sent), { message: 'a sent code block wears the same field as the composer\'s' })
      .toBe(composed)

    // ...and that is not a trivial match: the bubble it lands in paints a
    // DIFFERENT surface than the panel behind the composer. One declaration on
    // two hosts is the whole point of a field that composites.
    const bubbleSurface = await readAttached(bubble, 'the bubble surface', (matches) => {
      const el = matches.find(candidate => candidate.isConnected)
      return el ? getComputedStyle(el).backgroundColor : null
    })
    const panelSurface = await resolvedColor(page, 'var(--background)')
    expect(bubbleSurface, 'a user bubble is a different surface from the panel').not.toBe(panelSurface)

    // ...including a `<pre>` Shiki never reached. Both plain frames are
    // transient, so the field is measured on a bare `<pre>` placed in the same
    // markdown body rather than by racing the render: a selector narrowed back
    // to `pre.shiki` leaves this one on the app's own tint instead.
    const unhighlighted = await readAttached(sent, 'an un-highlighted <pre>', (matches) => {
      const el = matches.find(candidate => candidate.isConnected)
      if (!el)
        return null
      const bare = document.createElement('pre')
      el.parentElement!.append(bare)
      const painted = getComputedStyle(bare).backgroundColor
      bare.remove()
      return painted
    })
    expect(unhighlighted, 'an un-highlighted <pre> wears the same field').toBe(composed)
  })

  test('shows the block through, rather than tinting it twice, for a child that fills the background', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()
    await page.keyboard.type('```')
    const block = editor.locator('pre')
    await expect(block).toBeVisible()

    // A code surface repoints the app's own token names at the syntax theme's,
    // so a rule written inside one is themed by default. `--background` is the
    // trap: a block's field is a translucent tint, so a child that filled "the
    // background" with the field again would composite it twice and paint itself
    // a step darker than the block it sits in. Inside a block it must resolve to
    // `transparent` -- the block already painted it.
    const remapped = await block.evaluate((el) => {
      const probe = document.createElement('span')
      probe.style.backgroundColor = 'var(--background)'
      probe.style.borderTopColor = 'var(--border)'
      el.append(probe)
      const style = getComputedStyle(probe)
      const read = { background: style.backgroundColor, border: style.borderTopColor }
      probe.remove()
      return read
    })
    expect(remapped.background, 'a child filling the background shows the block through').toBe('rgba(0, 0, 0, 0)')

    // ...and a border inside the block steps from that field, not from the code
    // page: the copy button carries one, and against a field that takes its
    // host's colour the code page's own border measured 1.0005:1 on one palette.
    expect(colorAlpha(remapped.border), 'chrome inside a block outlines against the field').toBeLessThan(1)
  })

  test('gives inline code and a <kbd> the same step as a fenced block', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Inline code first, then a fenced block below it, so both are measured on
    // the same host in the same frame.
    await page.keyboard.type('`inline`')
    await page.keyboard.press('Enter')
    await page.keyboard.type('```')

    const inline = editor.locator('code').first()
    await expect(inline).toBeVisible()
    const block = editor.locator('pre')
    await expect(block).toBeVisible()

    // One rule paints every code element, at the strength a block's field is
    // built from, so the two cannot drift when either is tuned.
    expect(colorAlpha(await inline.evaluate(el => getComputedStyle(el).backgroundColor)))
      .toBeCloseTo(colorAlpha(await blockBackground(block)), 3)

    // The rule names three more tags that mean the same thing in HTML. None
    // renders today -- the markdown chain drops raw HTML and no component writes
    // one -- so a probe is what proves they are covered rather than left square
    // and unpainted for whoever adds the first one.
    const kbd = await page.evaluate(() => {
      const el = document.createElement('kbd')
      document.body.append(el)
      const style = getComputedStyle(el)
      const read = { background: style.backgroundColor, radius: style.borderTopLeftRadius }
      el.remove()
      return read
    })
    expect(colorAlpha(kbd.background), 'a <kbd> wears the same step').toBeCloseTo(CODE_BLOCK_TINT_PERCENT / 100, 4)
    expect(kbd.radius, 'a <kbd> is rounded rather than square').not.toBe('0px')
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

test.describe('send feedback button labels', () => {
  test('ExitPlanMode banner shows Reject when editor is empty and Send feedback when typing', async ({ page, authenticatedWorkspace }) => {
    // Enter plan mode, write a dummy plan, and exit
    const banner = await enterAndExitPlanMode(page)
    await expect(banner.getByText('Plan Ready for Review')).toBeVisible()

    const rejectBtn = page.locator('[data-testid="plan-reject-btn"]')

    // With empty editor, button should say "Reject"
    await expect(rejectBtn).toHaveText('Reject')

    // Type feedback into the editor. The button must switch to "Send feedback".
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await editor.click()
    await page.keyboard.type('Please reconsider this approach')
    await expect(rejectBtn).toHaveText('Send feedback')
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
