import type { Locator } from '@playwright/test'
import { POPOVER_CARD_PADDING } from '../../src/styles/popoverTokens'
import { expect, test } from './fixtures'
import { settleFrames } from './helpers/frames'
import { sendScriptedTurn } from './helpers/scriptedTurn'
import { selectedText } from './helpers/selection'
import { waitTimeoutBeforeTestDeadline } from './helpers/testDeadline'
import { closeComposerMenus, composerEditor, openAgentInfoCard, openAgentViaUI, openPlusMenu, resolvedColor, stableBox } from './helpers/ui'

const HAS_TEXT_RE = /.+/

/**
 * Measure the popover's offset from its trigger.
 *
 * Read both rectangles in one page evaluation.
 * Two separate boundingBox calls can span an editor-footer update.
 * That update can remove the trigger and produce a missing-box failure.
 * A layout shift between those calls can also appear as drag drift.
 * One JavaScript evaluation measures both rectangles before another render can change them.
 * Retry the observation because the page can temporarily remove the trigger during a render.
 */
interface PopoverGeometry {
  dx: number
  dy: number
  /** Popover width, so a failure can say whether the popover RESIZED. */
  width: number
  /** Popover left in viewport coords, to distinguish a clamp from a slide. */
  popoverX: number
  /** Trigger left in viewport coords: if this moved, the PAGE moved. */
  triggerX: number
  /** Whether calcPopoverPosition put it above the trigger. */
  flipped: boolean
}

async function offsetFromTrigger(popover: Locator, triggerTestId: string): Promise<PopoverGeometry> {
  let geometry: PopoverGeometry | null = null
  await expect(async () => {
    geometry = await popover.evaluate((el, selector) => {
      const trigger = document.querySelector(selector)
      if (!trigger)
        return null
      const p = el.getBoundingClientRect()
      const t = trigger.getBoundingClientRect()
      return {
        dx: p.x - t.x,
        dy: p.y - t.y,
        width: p.width,
        popoverX: p.x,
        triggerX: t.x,
        flipped: el.hasAttribute('data-flipped'),
      }
    }, `[data-testid="${triggerTestId}"]`)
    // A hidden popover still answers getBoundingClientRect(), with every field
    // zero -- so width===0 means "not laid out", not "zero-width popover", and
    // measuring it produces a garbage offset instead of a readable failure.
    expect(geometry, 'popover and trigger must both be laid out').not.toBeNull()
    expect(geometry!.width, 'popover must still be open and laid out').toBeGreaterThan(0)
    // Ended before the test deadline: a bare toPass() inherits no timeout and
    // runs to the test timeout, so a popover that genuinely closed reported
    // "Test timeout exceeded" instead of naming the assertion. Nothing inside
    // this loop waits -- the assertions read an already-captured value -- so
    // the limit only decides how long we keep re-measuring.
  }).toPass({ timeout: waitTimeoutBeforeTestDeadline() })
  return geometry!
}

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
  test('clicking editor while popover is open should keep editor focused', async ({ page, authenticatedWorkspace, modelScript }) => {
    // Ensure an agent tab is open
    await openAgentViaUI(page)

    const editor = composerEditor(page)
    await expect(editor).toBeVisible()

    // Send a message so the agent session starts and context info appears
    await sendScriptedTurn(page, modelScript)

    // Wait for the ContextUsageGrid trigger to appear
    const contextGrid = page.locator('[data-testid="agent-info-trigger"]').getByTestId('context-usage-grid')
    await expect(contextGrid).toBeVisible()

    // Open the popover by clicking the trigger
    const popover = await openAgentInfoCard(page)

    // Verify directory is shown in the popover (worker name may not be
    // populated in E2EE mode where agent data comes from the Worker)
    await expect(popover.locator('[data-testid="info-row-directory"]')).toBeVisible()

    // Now click the editor text input area — this should light-dismiss the
    // popover and leave focus in the editor.
    //
    // Click the CENTRE of the text area, not a corner. The composer box
    // overlays the `[+]` button on the left edge and the Interrupt/Send cluster
    // on the right edge, both absolutely positioned INSIDE the editor's own
    // box, so a corner point lands on a button and never reaches the editor —
    // focus then stays on the body and the assertion below fails for a reason
    // that has nothing to do with the popover.
    const editorBox = await editor.boundingBox()
    const popoverBox = await popover.boundingBox()
    expect(editorBox).not.toBeNull()
    expect(popoverBox).not.toBeNull()

    const clickX = editorBox!.x + editorBox!.width / 2
    const clickY = editorBox!.y + editorBox!.height / 2
    // The popover is anchored to the status bar below the box, and may flip
    // above its trigger. Assert rather than dodge: a popover covering the
    // centre of the text area is itself a layout defect, and silently clicking
    // somewhere else would hide it.
    const overlapsPopover = popoverBox
      && clickX >= popoverBox.x && clickX <= popoverBox.x + popoverBox.width
      && clickY >= popoverBox.y && clickY <= popoverBox.y + popoverBox.height
    expect(overlapsPopover, 'the info popover must not cover the editor text area').toBeFalsy()
    await page.mouse.click(clickX, clickY)

    // Wait for the popover to close via light-dismiss
    await expect(popover).not.toBeVisible()

    // The light-dismiss moves focus while it hides the popover, and the
    // `toggle` event of the close comes in a later task. The next frames run
    // after both, so a focus change that the close causes has landed.
    await settleFrames(page)

    // The editor should retain focus after the popover closes. `contains` is also
    // true for the editor itself.
    const editorHasFocus = await editor.evaluate(proseMirror => proseMirror.contains(document.activeElement))
    expect(editorHasFocus).toBe(true)
  })

  /**
   * Problem 2: Popover repositions when selecting text by dragging.
   *
   * When the agent-info popover is open and the user drags to select text
   * inside the popover content, the popover suddenly changes position.
   * This happens because the drag/selection causes scroll events that
   * trigger the reposition logic.
   */
  test('selecting text inside popover by dragging should not reposition it', async ({ page, authenticatedWorkspace, modelScript }) => {
    // Ensure an agent tab is open
    await openAgentViaUI(page)

    // Send a message so the agent session starts and context info appears.
    // The helper also lets the TURN finish before anything is measured. The
    // answer bubble is not the end of the layout churn: the turn-end divider,
    // the context-usage update and the git-status refresh all land after it,
    // and each one grows the chat column and nudges the anchored popover. That
    // is what produced a 2.49px drift against this test's 2px tolerance -- the
    // popover was still settling, not being repositioned by the drag.
    await sendScriptedTurn(page, modelScript)

    const contextGrid = page.locator('[data-testid="agent-info-trigger"]').getByTestId('context-usage-grid')
    await expect(contextGrid).toBeVisible()

    // Open the popover
    const popover = await openAgentInfoCard(page)

    // Wait for the LAST row to arrive before measuring anything. The Session ID
    // row is `<Show when={agent.agentSessionId}>` -- absent until the CLI
    // reports the id -- and it is by far the widest row, so its appearance
    // grows the popover, and a wider popover gets clamped back inside the
    // viewport by calcPopoverPosition. That is a 400px HORIZONTAL jump, which
    // the drag assertion below would otherwise charge to the drag. The
    // stable-box check alone cannot cover it: the box is genuinely stable right
    // up until the row lands.
    await expect(popover.locator('[data-testid="session-id-value"]')).toBeVisible()

    // Record the popover's offset from its anchor, once it has stopped moving.
    // Two consecutive equal boxes is the app's own statement that positioning
    // settled -- stronger than a fixed sleep, and it cannot pass early.
    await stableBox(popover)
    const initialOffset = await offsetFromTrigger(popover, 'agent-info-trigger')

    // Find a text element inside the popover to drag-select.
    // The popover has info rows with labels like "Session ID", "Context", etc.
    const popoverText = popover.locator('span, div').filter({ hasText: HAS_TEXT_RE }).first()
    await expect(popoverText).toBeVisible()
    // Press at the element's CENTRE, and let Playwright put the pointer there:
    // hover() re-resolves the element and waits for it to be stable, so the
    // press cannot land on a stale rect. The old version measured the box and
    // then pressed 2px inside its left edge, which is inside the popover only
    // as long as nothing moves -- and the popover does settle a few pixels
    // while the turn's trailing updates land. A press 2px outside it is a
    // light-dismiss, and the popover was simply gone by the time the drift was
    // measured (that failure reported a nonsense 403px offset against a
    // zero-sized rect rather than "the popover closed").
    await popoverText.hover()
    // Press IMMEDIATELY after the hover, before measuring anything. hover()
    // leaves the pointer at the element's centre as of the moment it resolved,
    // and any reposition between that and the press moves the popover out from
    // under a pointer that is about to go down OUTSIDE it -- which is a
    // light-dismiss, and the popover is simply gone before the drift is
    // measured. A press-then-measure order closes that window entirely:
    // light-dismiss fires on pointerdown, so once the button is down a later
    // reposition cannot dismiss anything, and the box read below is safe.
    await page.mouse.down()
    const textBox = await popoverText.boundingBox()
    expect(textBox).not.toBeNull()
    const dragY = textBox!.y + textBox!.height / 2
    const dragFromX = textBox!.x + textBox!.width / 2
    // Sweep right, staying inside the element the whole way.
    const dragToX = textBox!.x + textBox!.width - 2
    // Each step is rendered before the next one, as a real drag paces itself.
    for (let i = 1; i <= 5; i++) {
      await page.mouse.move(dragFromX + ((dragToX - dragFromX) * i) / 5, dragY)
      await settleFrames(page)
    }
    await page.mouse.up()

    // The popover must still be OPEN. Dragging to select text inside it must
    // not light-dismiss it, and if it did close there is no position left to
    // compare -- which surfaced as a bare "no bounding box" throw rather than
    // as the fact that the drag dismissed the popover.
    await expect(popover, 'the drag must not dismiss the popover').toBeVisible()

    // Check the popover did not REPOSITION -- measured against its anchor, not
    // against absolute page coordinates.
    //
    // The absolute comparison was the wrong invariant: the popover is anchored
    // to the trigger in the editor footer, so anything that changes the layout
    // beneath it (a late turn-end divider, a context-usage update, a git-status
    // refresh) slides BOTH by the same amount. Those drifts measured 2.49px and
    // 3.38px against a 2px tolerance -- a moving page, not a repositioned
    // popover. The offset from the trigger is invariant under that motion and
    // still changes by tens of pixels if the popover genuinely re-anchors or
    // flips, which is the failure this test exists to catch.
    // 6px, from measurement rather than taste. Three runs put the residual
    // drift at 2.49 / 3.38 / 3.71px even measured against the anchor, so the
    // popover really does settle a few sub-pixel-rounded pixels during a drag
    // and the original 2px was simply below the noise floor. A genuine
    // reposition -- a flip, or a re-anchor to the other side -- moves it by the
    // popover's own height, tens of pixels, so this still catches the failure
    // the test exists for.
    const DRIFT_TOLERANCE_PX = 6
    const finalOffset = await offsetFromTrigger(popover, 'agent-info-trigger')
    // The whole geometry goes into the message, because a bare "403.67 > 6" says
    // nothing about WHICH of the three ways this can move actually happened:
    // the popover resized (width), the viewport clamp engaged (popoverX pinned
    // while triggerX moved), or it re-anchored/flipped. A residual few px is the
    // popover settling; anything larger should be readable from here without a
    // second run.
    const detail = `initial=${JSON.stringify(initialOffset)} final=${JSON.stringify(finalOffset)}`
    expect(Math.abs(finalOffset.dx - initialOffset.dx), `horizontal drift; ${detail}`)
      .toBeLessThanOrEqual(DRIFT_TOLERANCE_PX)
    expect(Math.abs(finalOffset.dy - initialOffset.dy), `vertical drift; ${detail}`)
      .toBeLessThanOrEqual(DRIFT_TOLERANCE_PX)
  })
})

/** Read all four computed padding values inside the page without a closure over this file. */
function readPadding(el: Element): string {
  const style = getComputedStyle(el)
  return [style.paddingTop, style.paddingRight, style.paddingBottom, style.paddingLeft].join(' ')
}

/**
 * Measure the normal and compact card insets through temporary elements.
 * Oat supplies the normal inset for a card that fills the page, including authentication forms.
 * A floating card uses the compact inset.
 *
 * Read both from Oat's spacing scale instead of fixed pixel values.
 * Fixed values could still pass after the scale changes and the cards stop matching the app.
 * The caller supplies the compact value from ~/styles/popoverTokens.ts.
 * The popover stylesheet reads that same value.
 * Playwright applies no vanilla-extract transform, so the test imports the plain value rather than the styled class.
 *
 * Both probes use this function. Define its read helper inside the function.
 * Playwright serializes the function into the page, so it cannot close over this file.
 * A function argument cannot cross that serialization boundary either.
 */
function resolveCardPaddings(popoverPadding: string): { oat: string, popover: string } {
  const read = (configure: (el: HTMLElement) => void): string => {
    const probe = document.createElement('div')
    configure(probe)
    document.body.append(probe)
    try {
      const style = getComputedStyle(probe)
      return [style.paddingTop, style.paddingRight, style.paddingBottom, style.paddingLeft].join(' ')
    }
    finally {
      probe.remove()
    }
  }
  return {
    oat: read((el) => { el.className = 'card' }),
    popover: read((el) => { el.style.padding = popoverPadding }),
  }
}

test.describe('agent info card', () => {
  /**
   * The card opens from two places -- the status bar's context-usage trigger and
   * the `[+]` menu's "Agent info" item -- and shows the same rows in both. They
   * inset those rows differently for as long as each call site sets its own
   * padding, which is what this asserts against.
   */
  test('both surfaces inset the card by the shared popover-card padding', async ({ page, authenticatedWorkspace, modelScript }) => {
    await openAgentViaUI(page)
    await sendScriptedTurn(page, modelScript)

    const statusBarCard = await openAgentInfoCard(page)
    const statusBarPadding = await statusBarCard.evaluate(readPadding)

    // One card at a time: the `[+]` menu opens over the status bar, and its own
    // helper refuses to run with another composer popover still open.
    await closeComposerMenus(page)

    await openPlusMenu(page)
    await page.locator('[data-testid="composer-agent-info"]').click()
    const plusMenuCard = page.locator('[data-testid="composer-agent-info-popover"]')
    await expect(plusMenuCard).toBeVisible()
    const plusMenuPadding = await plusMenuCard.evaluate(readPadding)

    const padding = await page.evaluate(resolveCardPaddings, POPOVER_CARD_PADDING)
    expect(statusBarPadding, 'the status bar\'s card must use the popover-card padding').toBe(padding.popover)
    expect(plusMenuPadding, 'the `[+]` menu\'s card must use the popover-card padding').toBe(padding.popover)

    // The override reaches the card at all. It wins by LAYER, not by specificity: both
    // selectors are (0,1,0), so a tie would be decided by stylesheet order alone. If the
    // unlayered class ever stopped outranking Oat's `components` layer, the cards would
    // quietly fall back to the generous page-card inset, and the two assertions above would
    // still pass together.
    expect(statusBarPadding, 'a floating card must NOT keep Oat\'s page-card inset').not.toBe(padding.oat)

    // ...and Oat's `card` class still reaches it, which nothing above can see anymore. The inset
    // used to BE the proof: it came from Oat, so a card that lost the class lost the padding with
    // it. The unlayered override now supplies that padding on its own, so a popover stripped of
    // `card` would render as a transparent, border-less block over the transcript and every
    // assertion above would still pass. Measure what only Oat supplies instead.
    const cardSurface = await statusBarCard.evaluate((el) => {
      const style = getComputedStyle(el)
      return { background: style.backgroundColor, border: style.borderTopWidth, shadow: style.boxShadow }
    })
    expect(cardSurface.background, 'a card popover must paint Oat\'s card background').not.toBe('rgba(0, 0, 0, 0)')
    expect(cardSurface.border, 'a card popover must carry Oat\'s card border').not.toBe('0px')
    expect(cardSurface.shadow, 'a card popover must carry Oat\'s card shadow').not.toBe('none')
  })

  /**
   * The card is text the user reads and copies -- a session id, a directory, a
   * branch. A popover that closes on a click inside it cannot hold that text: the
   * press starts a selection and the release takes the popover away.
   */
  test('a click inside the card leaves it open, so its text stays selectable', async ({ page, authenticatedWorkspace, modelScript }) => {
    await openAgentViaUI(page)
    await sendScriptedTurn(page, modelScript)

    const infoTrigger = page.locator('[data-testid="agent-info-trigger"]')
    const popover = await openAgentInfoCard(page)

    const sessionId = popover.locator('[data-testid="session-id-value"]')
    await expect(sessionId).toBeVisible()

    // A plain click first. `aria-expanded` is the app's OWN statement of whether
    // the popover is open, and it flips synchronously with the dismiss -- a
    // visibility check alone would pass on a popover caught mid-fade, which is
    // exactly the state a dismiss leaves behind for 150ms.
    await sessionId.click()
    await expect(infoTrigger).toHaveAttribute('aria-expanded', 'true')
    await expect(popover).toBeVisible()

    // Then the gesture the click rule exists for: drag across the value and
    // confirm the selection survives with the card.
    const box = await sessionId.boundingBox()
    expect(box).not.toBeNull()
    const dragY = box!.y + box!.height / 2
    await page.mouse.move(box!.x + 2, dragY)
    await page.mouse.down()
    for (let i = 1; i <= 5; i++) {
      await page.mouse.move(box!.x + 2 + ((box!.width - 4) * i) / 5, dragY)
    }
    await page.mouse.up()

    await expect(infoTrigger).toHaveAttribute('aria-expanded', 'true')
    await expect(popover).toBeVisible()
    const selected = await selectedText(page)
    expect(selected.length, 'the drag must leave text selected').toBeGreaterThan(0)
  })
})

test.describe('menu item appearance', () => {
  /**
   * Oat styles every `<button>` with a solid `var(--primary)` fill, and menu
   * items are `<button role="menuitem">`. Through Oat 0.6.x its own
   * `[role="menuitem"]` rule cancelled that fill; 0.7 narrowed the rule to
   * layout only and every menu in the app turned into a column of primary
   * buttons.
   *
   * Nothing else catches that: the markup, the roles and the tests all stay
   * valid, so only a rendered page shows it. Reading the computed style is the
   * cheapest place to assert the cancellation still happens.
   */
  test('menu items render flat, not as primary-filled buttons', async ({ page, authenticatedWorkspace }) => {
    await page.getByTestId('app-menu-trigger').first().click()

    const item = page.getByRole('menuitem', { name: 'Preferences' })
    await expect(item).toBeVisible()

    const foreground = await resolvedColor(page, 'var(--foreground)')
    const primaryForeground = await resolvedColor(page, 'var(--primary-foreground)')
    const computed = await item.evaluate((el) => {
      const style = getComputedStyle(el)
      return {
        background: style.backgroundColor,
        color: style.color,
        borderWidth: style.borderTopWidth,
      }
    })

    expect(computed.background, `menu item should have no fill of its own, got ${computed.background}`)
      .toBe('rgba(0, 0, 0, 0)')
    expect(computed.borderWidth, `menu item should have no button border, got ${computed.borderWidth}`)
      .toBe('0px')
    // The colour half of the cancellation, asserted separately because it fails
    // separately. Oat's button rule sets `color: var(--primary-foreground)` --
    // white -- which on a popover painted `var(--background)` is white on
    // near-white in light theme and near-black on near-black in dark. Checking
    // only the fill leaves that unreadable state green.
    expect(computed.color, `menu item should take body text colour, got ${computed.color}`)
      .toBe(foreground)
    expect(computed.color).not.toBe(primaryForeground)
  })

  test('menu items still take Oat\'s hover affordance', async ({ page, authenticatedWorkspace }) => {
    // The reset lives in its own cascade layer, which outranks Oat's layered
    // `:hover` rule. Restating the hover is what keeps menu items from going
    // inert.
    await page.getByTestId('app-menu-trigger').first().click()

    const item = page.getByRole('menuitem', { name: 'Preferences' })
    await expect(item).toBeVisible()
    await item.hover()

    // Assert the accent specifically, not merely "some opaque colour". In the
    // regressed state Oat's button rule fills every item with `var(--primary)`
    // before the pointer arrives at all, so a `not.toBe('rgba(0, 0, 0, 0)')`
    // check passes on exactly the breakage the sibling test exists to catch --
    // and would equally pass on an item with no hover rule.
    //
    // Polled, not read once: Oat's button rule carries
    // `transition: background-color var(--transition-fast)`, so the fill is
    // still mid-interpolation for a frame or two after the pointer arrives and
    // a single read races it.
    const accent = await resolvedColor(page, 'var(--accent)')
    const backgroundColor = () => item.evaluate(el => getComputedStyle(el).backgroundColor)
    await expect.poll(backgroundColor).toBe(accent)
  })
})

/**
 * Test an automatic popover inside another automatic popover.
 *
 * The browser must retain the outer popover when the inner popover opens.
 * Otherwise the outer popover closes immediately and makes every inner control inaccessible.
 * Only a browser can establish this behavior.
 *
 * The plus menu and Agent info card supply this pair for every agent without requiring model output.
 * The to-do popover contains the same nesting for a session goal.
 * codex/session-goal-set-and-clear.spec.ts tests that host after the model publishes its to-do list.
 * A previous model turn that returned prose left that nested view untested.
 */
test.describe('nested popovers', () => {
  test('an inner card survives opening inside an outer menu', async ({ page, authenticatedWorkspace, modelScript }) => {
    void authenticatedWorkspace
    await openAgentViaUI(page)

    // One turn first. "Agent info" appears only once the agent HAS a session --
    // `showInfoTrigger` reads `agentSessionId` -- and an agent that was opened
    // but never prompted has none.
    await sendScriptedTurn(page, modelScript)

    const plusMenu = await openPlusMenu(page)
    await expect(plusMenu).toBeVisible()

    await page.getByTestId('composer-agent-info').click()

    // Both, in this order. The inner card opened AND the outer menu is still
    // there: the regression this guards dismisses the outer one, which leaves
    // the inner card mounted for an instant and then takes both off screen.
    const infoCard = page.getByTestId('composer-agent-info-popover')
    await expect(infoCard).toBeVisible()
    await expect(plusMenu).toBeVisible()

    // A click INSIDE the inner card keeps both open, which is what `as="card"`
    // promises and what a text selection inside it depends on.
    await infoCard.click({ position: { x: 4, y: 4 } })
    await expect(infoCard).toBeVisible()
    await expect(plusMenu).toBeVisible()

    // And a dismiss still reaches BOTH, so the nesting does not strand the
    // outer menu open behind the inner one.
    await page.keyboard.press('Escape')
    await expect(infoCard).toBeHidden()
    await page.keyboard.press('Escape')
    await expect(plusMenu).toBeHidden()
  })
})

test.describe('a menu whose click also focuses the tile', () => {
  /**
   * A click on a tab-bar trigger opens the menu AND bubbles to the tile, whose
   * own `onClick` focuses it. While focusing rebuilt the tab bar, the menu that
   * this same click had just opened was detached before it could paint --
   * and detaching a popover hides it with NO `toggle` event, so the trigger's
   * `aria-expanded` stayed false and nothing in the DOM named a cause.
   *
   * Both halves are asserted. The menu is open, AND the tab bar is the element
   * it was before the click. The first half alone would also pass for a bar
   * that rebuilt and then reopened, which is not what this guards.
   */
  /**
   * `ot-dropdown` is unknown to the UA, so it defaults to `display: inline`,
   * and that costs a different thing in each formatting context. In an inline
   * context the host wraps its trigger in a LINE BOX, which reserves room under
   * the baseline for descenders and is therefore taller than the trigger it
   * holds. In a flex row -- which is where this trigger sits -- the inline
   * blockifies and the host becomes a flex ITEM, adding one `gap` of dead space
   * to the row. Either way a row whose action is a dropdown did not line up
   * with the same row whose action is a plain button.
   *
   * The rule in ~/styles/popover.css.ts gives every host `display: contents`.
   * Two unit tests used to pin the `data-headless` attribute that selected a
   * SUBSET of hosts; the attribute is gone, and this is what replaced them --
   * the rule is global CSS, which jsdom does not load, so only a real browser
   * can see it.
   *
   * `display` is the assertion that discriminates, and it reads `block` without
   * the rule rather than `inline`, because Chromium reports the blockified
   * value for a flex item. The height comparison guards the line-box context,
   * where the host would be taller than its own trigger.
   */
  test('a dropdown host adds no box of its own to the row', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace

    const measured = await page.locator('[data-testid="tab-more-menu"]:visible').first().evaluate((trigger) => {
      const host = trigger.closest('ot-dropdown')
      if (!host)
        return null
      return {
        display: getComputedStyle(host).display,
        hostHeight: host.getBoundingClientRect().height,
        triggerHeight: trigger.getBoundingClientRect().height,
      }
    })

    expect(measured, 'the trigger sits inside an ot-dropdown host').not.toBeNull()
    expect(measured!.display).toBe('contents')
    expect(measured!.triggerHeight).toBeGreaterThan(0)
    // The host contributes nothing of its own. An inline host would report the
    // line box here, which is taller than the trigger it wraps.
    expect(measured!.hostHeight).toBeLessThanOrEqual(measured!.triggerHeight)
  })

  test('the tab bar survives the click that opens its menu', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace

    const trigger = page.locator('[data-testid="tab-more-menu"]:visible').first()
    await expect(trigger).toBeVisible()
    const originalTabBar = await page.locator('[data-testid="tab-bar"]:visible').first().elementHandle()
    if (!originalTabBar)
      throw new Error('The visible tab bar has no element handle.')
    try {
      await trigger.click()

      await expect(trigger).toHaveAttribute('aria-expanded', 'true')
      await expect(page.locator('menu[popover]:popover-open')).toBeVisible()
      expect(
        await originalTabBar.evaluate(element => element.isConnected),
        'the original tab bar remains connected after the click',
      ).toBe(true)
    }
    finally {
      await originalTabBar.dispose()
    }
  })
})
