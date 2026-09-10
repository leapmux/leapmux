import type { Locator, Page } from '@playwright/test'
import type { FileSortOrder } from '../../../src/lib/fileSort'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'

import { expect } from '@playwright/test'
import { accountStorageKey, getTtlForKey, KEY_BROWSER_PREFS, PREFIX_EDITOR_DRAFT, PREFIX_FILES_SORT_ORDER } from '../../../src/lib/browserStorage'
import { solveCaptchaViaUI } from './captcha'
import { readEntry, storageKeys, writeEntry } from './storage'

/** Check if a locator is visible, returning false on timeout or error. */
export async function isMaybeVisible(locator: Locator, timeout?: number): Promise<boolean> {
  return locator.isVisible(timeout != null ? { timeout } : undefined).catch(() => false)
}

/** Wait until at least one locator in the list is visible. */
export async function expectAnyVisible(...locators: Locator[]) {
  await expect.poll(async () => {
    const visibility = await Promise.all(locators.map(locator => isMaybeVisible(locator)))
    return visibility.some(Boolean)
  }).toBe(true)
}

// ──────────────────────────────────────────────
// Sidebar text clipping
// ──────────────────────────────────────────────

/**
 * Check the composed styles for single-line label clipping in a real browser.
 * The vanilla-extract composition supplies multiple classes and rules. jsdom does not load their stylesheets.
 * Check min-width also. A flex item with min-width:auto retains its text width and prevents ellipsis.
 * Pair this with expectClipsLongText to verify the resulting layout.
 * Pass the label itself. A Tooltip wrapper contains the same text but uses display:contents and reports text-overflow:clip.
 */
export async function expectClipsToOneLine(label: Locator) {
  await expect(label).toHaveCSS('white-space', 'nowrap')
  await expect(label).toHaveCSS('text-overflow', 'ellipsis')
  await expect(label).toHaveCSS('overflow-x', 'hidden')
  await expect(label).toHaveCSS('min-width', '0px')
}

/**
 * Check that a long label clips without widening an ancestor scroller.
 * Style declarations alone cannot detect a container that grows to its widest row.
 * Temporarily replace the text and restore it within one synchronous browser operation. This forces layout while retaining Solid references to the same node.
 * Check ancestors with overflow-x:auto or scroll. The label itself must have scrollWidth greater than clientWidth to clip.
 * Allow one pixel for subpixel rounding in ancestor measurements.
 */
export async function expectClipsLongText(label: Locator) {
  const measured = await label.evaluate((el) => {
    const node = el.firstChild
    if (!(node instanceof Text))
      throw new TypeError('expectClipsLongText needs a label whose first child is its text')
    const original = node.nodeValue
    // A repeated letter has no break opportunity, which is the input that
    // escaped its box and grew the sideways scrollbar in the first place.
    node.nodeValue = 'W'.repeat(200)
    try {
      const scrolling: string[] = []
      for (let box: Element | null = el; box && box !== document.body; box = box.parentElement) {
        const { overflowX } = getComputedStyle(box)
        if (overflowX !== 'auto' && overflowX !== 'scroll')
          continue
        if (box.scrollWidth - box.clientWidth > 1)
          scrolling.push(`${box.tagName.toLowerCase()}.${box.getAttribute('class') ?? ''} (${box.scrollWidth} > ${box.clientWidth})`)
      }
      return { clipped: el.scrollWidth - el.clientWidth > 1, scrolling }
    }
    finally {
      node.nodeValue = original
    }
  })
  expect(measured.clipped, 'the label must clip its own text').toBe(true)
  expect(measured.scrolling, 'no ancestor should scroll horizontally').toEqual([])
}

// ──────────────────────────────────────────────
// Common UI interaction helpers
// ──────────────────────────────────────────────

/**
 * Send a message through the ProseMirror editor with no inter-key delay.
 * ProseMirror handles the ordered key events synchronously. The former 100ms delay added about five seconds to each arithmetic prompt.
 * Tests of input rules, mention triggers, and slash commands retain their deliberate local typing intervals.
 */
export async function sendMessage(page: Page, text: string) {
  const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
  await expect(editor).toBeVisible()
  await editor.click()
  await page.keyboard.type(text)
  await page.keyboard.press('Meta+Enter')
  // Wait for the composer to clear after it accepts the send. This prevents the caller from proceeding before that local acknowledgement.
  await expect(editor).toHaveText('')
}

/** Wait for the control request banner to appear and return a scoped locator. */
export async function waitForControlBanner(page: Page) {
  const banner = page.locator('[data-testid="control-banner"]')
  await expect(banner).toBeVisible()
  return banner
}

/**
 * ChatView mounts hidden copies of rows whose heights remain unknown.
 * ChatHiddenPremeasure retains the same IDs, text, and classes. A visible-list row can also stay hidden until measurement completes.
 * An unfiltered locator can therefore match multiple copies and fail Playwright strict mode.
 * A 20ms sample once found six bubbles for two messages, including four hidden copies. Higher load can lengthen that measurement period.
 * Apply :visible to the outermost chat locator. Descendants of a visible bubble need no extra filter.
 * Use these shared helpers for chat locators.
 */
const VISIBLE = ':visible'

/**
 * CSS selector for agent bubbles without a visibility filter.
 * Use it only below an already visible element or as a relative has filter.
 * A page-level lookup also matches hidden measurement copies. Use assistantBubbles for that lookup.
 */
export const ASSISTANT_BUBBLE_SELECTOR = '[data-testid="message-bubble"][data-role="agent"]'

/** CSS selector for user message bubbles. Same caveat as {@link ASSISTANT_BUBBLE_SELECTOR}. */
export const USER_BUBBLE_SELECTOR = '[data-testid="message-bubble"][data-role="user"]'

/** Return a locator for all visible assistant message bubbles. */
export function assistantBubbles(page: Page) {
  return page.locator(ASSISTANT_BUBBLE_SELECTOR + VISIBLE)
}

/** Return a locator for all visible user message bubbles. */
export function userBubbles(page: Page) {
  return page.locator(USER_BUBBLE_SELECTOR + VISIBLE)
}

/** Return a locator for all visible message bubbles, whatever their role. */
export function messageBubbles(page: Page) {
  return page.locator(`[data-testid="message-bubble"]${VISIBLE}`)
}

/** Return a locator for all visible message content nodes. */
export function messageContents(page: Page) {
  return page.locator(`[data-testid="message-content"]${VISIBLE}`)
}

/**
 * Locate visible message-band rows. Restrict kind to text or thought when needed.
 * The row supplies the full-width background and border, so the marker belongs to the row.
 * The test attribute avoids dependence on hashed style class names.
 */
export function bandRows(page: Page, kind?: 'text' | 'thought') {
  const selector = kind === undefined ? '[data-band]' : `[data-band="${kind}"]`
  return page.locator(selector + VISIBLE)
}

/** The chat's scrolling element, which the app publishes for exactly this purpose. */
export const CHAT_SCROLL_CONTAINER = '[data-chat-scroll-container="true"]'

/** Return a locator for the chat's scrolling element. */
export function chatScrollContainer(page: Page) {
  return page.locator(CHAT_SCROLL_CONTAINER)
}

/**
 * Maximum wait for an attached match in readAttached.
 * A row replacement takes a few frames. Keep this below expect.timeout so an absent chat row produces a specific error before the test deadline.
 */
const ATTACHED_READ_TIMEOUT_MS = 15_000

/**
 * Read from an attached locator match. Use this for chat rows and their descendants because the app can replace them.
 * Locator resolution and evaluation require separate browser requests. A replacement between those requests leaves a detached node.
 * Detached nodes report zero geometry and empty styles. That can fail a layout check or falsely pass a color comparison.
 * See https://github.com/leapmux/leapmux/issues/402.
 *
 * evaluateAll also resolves an array handle before evaluating it. A replacement can occur between these two requests also.
 * A single page.evaluate request would lose Playwright visibility filtering, which excludes the hidden measurement copies.
 * Instead, the reader skips detached candidates and returns null for another attempt.
 * Every returned measurement then comes from one synchronous browser operation on an attached node.
 *
 * The serialized reader must filter the candidate array itself. It cannot call a Node closure across the browser boundary.
 * For the same reason, this helper passes CHAT_SCROLL_CONTAINER as the second reader argument.
 * A reader that does not need it can omit that parameter.
 */
export async function readAttached<R>(
  locator: Locator,
  what: string,
  read: (possiblyDetachedMatches: (SVGElement | HTMLElement)[], chatScrollContainerSelector: string) => R | null,
): Promise<R> {
  // Held on an object rather than in a `let`: the assignment happens inside the
  // retry closure, which control-flow narrowing cannot see through.
  const held: { value: R | null } = { value: null }
  await expect(async () => {
    held.value = await locator.evaluateAll(read, CHAT_SCROLL_CONTAINER)
    expect(held.value, `${what}: matched no element that was still in the document`).not.toBeNull()
  }).toPass({ timeout: ATTACHED_READ_TIMEOUT_MS })
  return held.value!
}

/**
 * Read all geometry for the two chat measurements in one browser operation.
 * Both callers use this reader and select the fields they need, so their layout measurements cannot diverge.
 */
interface ChatBoxGeometry {
  /** The element's own border-box width. */
  width: number
  /**
   * The list width used to calculate both gaps.
   * Include it in errors to distinguish the wrong scroller from an incorrect card width.
   * Derive the card width from listWidth minus both gaps.
   */
  listWidth: number
  /** Distance from the element's right side to the list's padding-box right edge. */
  rightGap: number
  /** Distance from the list's padding-box left edge to the element's left side. */
  leftGap: number
  /** Distance from the element's top edge down from its ROW's top edge, null when it has no parent. */
  topGapInRow: number | null
  /** The element's computed `border-top-right-radius`, as CSS reports it. */
  radius: string
}

/** A match that cannot be measured, and what it turned out to be instead. */
interface ChatBoxOutsideList {
  outside: string
}

/**
 * Measure an attached element against its own row and chat scroll container.
 * Find the container through its published attribute on an ancestor. Do not infer it from DOM position or computed overflow.
 * clientLeft accounts for its left border. clientWidth excludes the native scrollbar.
 * All values come from one layout, so a resize cannot split the measurement.
 */
function readChatBoxGeometry(
  possiblyDetachedMatches: (SVGElement | HTMLElement)[],
  chatScrollContainerSelector: string,
): ChatBoxGeometry | ChatBoxOutsideList | null {
  // A row that remounted between the resolve and this read is gone for good, and
  // reports a zero rect and an empty computed style rather than an error.
  const el = possiblyDetachedMatches.find(candidate => candidate.isConnected)
  if (!el)
    return null
  const list = el.closest(chatScrollContainerSelector)
  if (!list) {
    // Report a hidden measurement copy explicitly. It sits outside the chat scroller and can otherwise resemble a detached row.
    if (el.closest('[data-chat-premeasure-root="true"]'))
      return { outside: 'it is ChatView\'s hidden premeasure copy, not the live row' }
    const chain: string[] = []
    for (let node: Element | null = el; node && chain.length < 8; node = node.parentElement) {
      const testId = node.getAttribute('data-testid')
      chain.push(node.tagName.toLowerCase() + (testId ? `[${testId}]` : ''))
    }
    return { outside: `ancestors: ${chain.join(' < ')}` }
  }
  const listRect = list.getBoundingClientRect()
  const self = el.getBoundingClientRect()
  const row = el.parentElement
  const padLeft = listRect.left + list.clientLeft
  return {
    width: self.width,
    listWidth: list.clientWidth,
    rightGap: padLeft + list.clientWidth - self.right,
    leftGap: self.left - padLeft,
    topGapInRow: row ? self.top - row.getBoundingClientRect().top : null,
    radius: globalThis.getComputedStyle(el).borderTopRightRadius,
  }
}

/** Run {@link readChatBoxGeometry} through {@link readAttached} and reject a match it cannot measure. */
async function measureChatBox(locator: Locator, what: string): Promise<ChatBoxGeometry> {
  const read = await readAttached(locator, what, readChatBoxGeometry)
  // Not retried: an element outside every chat list stays outside, so looping on
  // it would replace this message with a test timeout 15 seconds later.
  if ('outside' in read)
    throw new Error(`${what}: element is not inside the chat scroll container -- ${read.outside}`)
  return read
}

/**
 * Measure a full-bleed chat element against the width it must span: the chat
 * scroll container's `clientWidth`, which IS its padding box (the scrollbar
 * excluded), and which is exactly what a band or a turn-end rule reaches.
 */
export async function measureAgainstChatList(locator: Locator): Promise<{ width: number, listWidth: number }> {
  const { width, listWidth } = await measureChatBox(locator, 'measureAgainstChatList')
  return { width, listWidth }
}

/** Where an end-of-line card's sides sit, and the corner it turns at the edge. */
export interface BubbleEdges {
  /**
   * The list's padding-box width, which the two gaps are measured against.
   *
   * Carried so a failure reports the SHAPE and not just the symptom: with it, a
   * card measured against the wrong list is one glance away from a card the
   * bleed rule simply missed. The card's own width is `listWidth - leftGap -
   * rightGap`, so it is not repeated here.
   */
  listWidth: number
  /** Distance from the card's right side to the list's padding-box right edge. */
  rightGap: number
  /** Distance from the list's padding-box left edge to the card's left side. */
  leftGap: number
  /** Distance from the card's top edge down from its ROW's top edge. */
  topGapInRow: number
  /** The card's computed `border-top-right-radius`, as CSS reports it. */
  radius: string
}

/**
 * Measure the card against both panel edges and its row.
 * User messages and plan execution cards share the right-alignment rule, so both tests use this reader.
 * topGapInRow checks that the card starts at the top of its row.
 * A bubble alignSelf rule can change that position. A real browser is required because jsdom does not calculate flex layout.
 */
export async function measureBubbleEdges(locator: Locator): Promise<BubbleEdges> {
  const { listWidth, rightGap, leftGap, topGapInRow, radius } = await measureChatBox(locator, 'measureBubbleEdges')
  if (topGapInRow === null)
    throw new Error('measureBubbleEdges: element has no row to measure against')
  return { listWidth, rightGap, leftGap, topGapInRow, radius }
}

/**
 * Restrict `locator` to the elements the user can see.
 *
 * For page-rooted chat assertions that match by text rather than test id --
 * `getByText` matches the hidden premeasure copy just as readily as the real
 * row.
 */
export function visibleOnly(locator: Locator): Locator {
  return locator.filter({ visible: true })
}

/** Return a locator for the first visible assistant message bubble. */
export function firstAssistantBubble(page: Page) {
  return assistantBubbles(page).first()
}

/** Return a locator for the last visible assistant message bubble. */
export function lastAssistantBubble(page: Page) {
  return assistantBubbles(page).last()
}

/**
 * Locate the first assistant message row that offers quote and copy actions.
 * Turn dividers and startup notices also use agent-role bubbles, but supply no onReply handler or selectable message prose.
 * Their order can vary, so the first agent bubble does not necessarily contain an assistant message.
 * Use this helper for tests that quote or select a message.
 */
export function firstAssistantMessageRow(page: Page) {
  return assistantBubbles(page)
    .locator('..')
    .filter({ has: page.locator('[data-testid="message-quote"]') })
    .first()
}

/**
 * Standard arithmetic chat probe shared across the agent e2e specs. The answer
 * (6912) is a distinctive 4-digit number that won't match incidental UI text
 * (model names like gpt-5.4, durations, token counts, dates) the way a single
 * digit would.
 */
export const ARITHMETIC_PROMPT = 'What is 1234 + 5678? Reply with just the number.'

/**
 * Matches the {@link ARITHMETIC_PROMPT} answer, tolerating a thousands comma.
 * Word-boundary anchored so it can't match 6912 as a substring of a larger
 * number (a token count, duration, or id) that incidentally contains it.
 */
export const ARITHMETIC_ANSWER = /\b6,?912\b/

/**
 * Arithmetic prompt for a second turn with a distinct answer.
 * Neither 3333 nor 6912 contains the other, so one answer cannot satisfy an assertion for the other turn.
 */
export const SECOND_ARITHMETIC_PROMPT = 'What is 1111 + 2222? Reply with just the number, nothing else.'

/** Matches the {@link SECOND_ARITHMETIC_PROMPT} answer. See {@link ARITHMETIC_ANSWER}. */
export const SECOND_ARITHMETIC_ANSWER = /\b3,?333\b/

/**
 * Assert the agent answered {@link ARITHMETIC_PROMPT}: the answer appears in
 * SOME assistant bubble. Scanning every bubble (rather than only the last one)
 * is robust to a trailing "Turn ended" result divider, which is itself an
 * agent-role bubble and would otherwise be picked up by lastAssistantBubble().
 */
export async function expectAssistantAnswer(page: Page, opts?: { answer?: RegExp, timeout?: number }) {
  const answer = opts?.answer ?? ARITHMETIC_ANSWER
  const matches = assistantBubbles(page).filter({ hasText: answer })
  await expect(matches).not.toHaveCount(0, opts?.timeout != null ? { timeout: opts.timeout } : undefined)
}

/**
 * Assert SOME visible user bubble contains `text` -- the mirror of
 * {@link expectAssistantAnswer} for the prompt side, used by the restart specs
 * to check that history survived.
 */
export async function expectUserMessage(page: Page, text: string) {
  await expect(userBubbles(page).filter({ hasText: text })).not.toHaveCount(0)
}

/**
 * Maximum wait for the thinking indicator to appear after a send.
 * The indicator may finish before the wait starts. A short observation period avoids a full action timeout in that case.
 */
const APPEARANCE_PROBE_MS = 2000

/** Wait for the agent to finish its current turn (thinking indicator gone). */
export async function waitForAgentIdle(page: Page, timeoutMs = 120_000) {
  const thinking = page.locator('[data-testid="thinking-indicator"]')
  // Observe the indicator before checking that it is hidden. An immediate absence check could precede the start of a turn.
  // An expired observation is permitted because a fast turn can finish before the first check.
  // Use the short explicit interval instead of the 30-second action timeout.
  // This helper alone cannot distinguish a completed turn from one that starts after the observation interval.
  // Callers must also check the expected response or operation result.
  await thinking.waitFor({ state: 'visible', timeout: APPEARANCE_PROBE_MS }).catch(() => {})
  await expect(thinking).not.toBeVisible({ timeout: timeoutMs })
}

// ──────────────────────────────────────────────
// UI helpers
// ──────────────────────────────────────────────

/**
 * Locate the app-menu trigger for the active layout.
 * Desktop uses app-menu-trigger in the title bar. Phones use collapsed-new-tab-button in the tab bar.
 * AppShell renders these only after AuthGuard permits access. Their presence also identifies the authenticated shell for login and startup tests.
 */
export function appMenuTrigger(page: Page): Locator {
  return page.getByTestId('app-menu-trigger').first().or(page.getByTestId('collapsed-new-tab-button')).first()
}

/**
 * Open the app menu for the active layout. Wait for either trigger before selecting one.
 * isVisible does not wait. Checking it immediately after navigation could select the hidden phone trigger before the desktop title bar mounts.
 */
async function openAppMenu(page: Page) {
  const appMenu = page.getByTestId('app-menu-trigger').first()
  const collapsed = page.getByTestId('collapsed-new-tab-button')
  await appMenuTrigger(page).waitFor({ state: 'visible' })
  if (await appMenu.isVisible())
    await appMenu.click()
  else
    await collapsed.click()
}

/**
 * Open the About dialog from the app menu.
 *
 * The item's label differs between the desktop shell and the browser ("About
 * LeapMux Desktop..." against "About..."), so it is matched by prefix.
 */
export async function openAboutDialog(page: Page): Promise<Locator> {
  await openAppMenu(page)
  await page.getByRole('menuitem', { name: /^About/ }).click()
  const dialog = page.getByRole('dialog', { name: 'About' })
  await expect(dialog).toBeVisible()
  return dialog
}

/**
 * Open Preferences and select a category.
 * Desktop uses sidebar tabs, and phones use a section menu.
 * category specifies the navigation ID. If absent, keep the dialog default.
 */
export async function openPreferencesDialog(page: Page, category?: string) {
  const dialog = page.getByRole('dialog', { name: 'Preferences' })
  // The prefs query parameter restores the open dialog and category after reload.
  // Reuse that dialog when it is already open. Its modal overlay makes the app-menu trigger inert.
  if (!(await dialog.isVisible())) {
    await openAppMenu(page)
    await page.getByRole('menuitem', { name: 'Preferences' }).click()
  }
  await expect(dialog).toBeVisible()
  if (category) {
    const item = dialog.getByTestId(`preferences-nav-${category}`)
    const compactTrigger = dialog.getByTestId('preferences-nav')
    // Wait for the category tab or compact menu trigger. Admin categories appear only after ListSettings responds.
    // An early check could choose a compact trigger that the desktop layout never renders.
    await item.or(compactTrigger).first().waitFor({ state: 'visible' })
    // Compact (phone) keeps the sections inside a closed dropdown; open it
    // first. Desktop tabs are already visible in the sidebar.
    if (!(await item.isVisible()))
      await compactTrigger.click()
    await item.click()
  }
}

/** Sign-in attempts {@link loginViaUI} makes before it gives up. */
const LOGIN_ATTEMPTS = 3
/**
 * Maximum duration of one sign-in attempt. Three attempts must fit within the 300-second test deadline.
 * Default URL and shell waits can consume 150 seconds together, leaving no time for the third attempt or a specific error.
 */
const LOGIN_ATTEMPT_TIMEOUT_MS = 60_000

/**
 * Login via the UI form. Navigates to /login, fills credentials, solves the
 * captcha, and returns once the authenticated app shell is on screen.
 */
export async function loginViaUI(page: Page, username = 'admin', password = 'admin123') {
  await page.goto('/login')
  await page.getByLabel('Username').fill(username)
  await page.getByLabel('Password').fill(password)
  await solveCaptchaViaUI(page)
  await page.getByRole('button', { name: 'Sign in' }).click()

  // Login selects /. Workspace activation does not change the path, so the URL check can match exactly.
  // Wait for the authenticated shell trigger. Do not wait for networkidle after an ALTCHA challenge.
  //
  // ALTCHA starts up to 16 solver workers and terminates the others after the first solution.
  // Chromium can cancel unfinished worker-script loads while Playwright retains their in-flight request records.
  // The idle timer then never starts, and an unlimited navigation wait lasts until the test deadline.
  // Faster solutions increase this overlap. One SCRYPT run finished while seven of ten workers still loaded their scripts.
  //
  // Retry a transient login refusal, such as a database that is not ready after restart.
  const loggedInURL = /\/$/
  let lastError: unknown
  for (let attempt = 0; attempt < LOGIN_ATTEMPTS; attempt++) {
    try {
      await expect(page).toHaveURL(loggedInURL, { timeout: LOGIN_ATTEMPT_TIMEOUT_MS })
      await appMenuTrigger(page).waitFor({ state: 'visible', timeout: LOGIN_ATTEMPT_TIMEOUT_MS })
      return // success
    }
    catch (err) {
      lastError = err
      // The test timeout tears the context down mid-wait. Probing a closed
      // page throws a second, unrelated error that buries the first one.
      if (page.isClosed())
        break
      // Check if there's an error message on the login page
      const error = page.locator('[class*="error"], [class*="Error"]')
      if (await error.count().catch(() => 0) > 0) {
        // Transient error — retry sign-in. The rejected submit consumed the
        // captcha payload and LoginPage.handleSubmit resets the field, so
        // `blocksSubmit()` disables the button until a fresh challenge is
        // solved. Re-solve BEFORE the click, or the click waits out the
        // action timeout on a disabled button.
        try {
          await solveCaptchaViaUI(page)
          await page.getByRole('button', { name: 'Sign in' }).click()
        }
        catch (resubmitError) {
          // The form cannot be resubmitted, so another attempt reports the
          // same failure. Report THIS error, which names the step that broke.
          lastError = resubmitError
          break
        }
      }
      // No error visible — the page may still be loading; the next iteration
      // checks the URL again.
    }
  }
  // Every attempt failed. Carry the last one's error: the retry loop is the
  // only thing that saw it, and a bare message here hid which wait expired.
  throw new Error(
    `loginViaUI: the app shell never mounted after ${LOGIN_ATTEMPTS} sign-in attempts`,
    { cause: lastError },
  )
}

/**
 * Navigate to the registration page and approve the worker.
 */
export async function approveWorkerViaUI(page: Page, token: string, name: string) {
  await page.goto(`/register/${token}`)
  await expect(page.getByRole('heading', { name: 'Approve Worker' })).toBeVisible()
  await page.getByPlaceholder('e.g. my-workstation').fill(name)
  await page.getByRole('button', { name: 'Approve' }).click()
  await expect(page.getByText('Worker Registered Successfully')).toBeVisible()
}

/**
 * Open a new agent in the currently selected workspace.
 * Clicks the agent button in the tab bar which directly creates an agent.
 */
export async function openAgentViaUI(page: Page) {
  // Wait for the active tab directory before clicking. The agent handler reads workerId and workingDir synchronously.
  // If either is absent, it opens the directory dialog and does not retry.
  // An early click therefore creates no tab and would leave the later count check waiting until timeout.
  await waitForActiveTabContext(page)
  // Count existing agent tabs so we can wait for the new one to appear.
  const tabsBefore = await page.locator('[data-testid="tab"][data-tab-type="agent"]').count()
  await page.locator('[data-testid^="new-agent-button"]').first().click()
  // Wait for the new agent tab to appear (the API call is async)
  await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(tabsBefore + 1)
  // Wait for the new tab to become selected and its editor to be ready
  await expect(page.locator('[data-testid="tab"][data-tab-type="agent"][aria-selected="true"]')).toBeVisible()
  await expect(page.locator('[data-testid="composer-editor"] .ProseMirror')).toBeVisible()
}

/**
 * Wait for the active tab working directory.
 * A projected tab initially holds only tile, position, and worker data. useTabHydrators retrieves the directory later.
 * Check the nonempty data-working-dir that the Files section publishes after resolution.
 * The tree root is insufficient: it can render from workerId alone with a ~ directory fallback.
 *
 * Wait for attachment, not visibility. Mobile layout can collapse the sidebar even when the directory is ready.
 * Report a timeout here so a hydration failure does not appear as an unrelated interaction failure.
 */
export async function waitForActiveTabContext(page: Page) {
  await page.locator('[data-working-dir]:not([data-working-dir=""])').first().waitFor({ state: 'attached' })
}

/**
 * Open a terminal through the tab-bar button. Wait for the active tab directory first.
 * The handler reads the directory synchronously. If absent, it opens a directory dialog without retrying.
 * An early click therefore creates no terminal, and later terminal assertions would time out.
 */
export async function openTerminalViaUI(page: Page) {
  await waitForActiveTabContext(page)
  await page.locator('[data-testid="new-terminal-button"]').click()
}

/**
 * Sign up a new user via the signup form.
 */
export async function signUpViaUI(page: Page, username: string, password: string, displayName = '', email = '') {
  await page.goto('/signup')
  await page.getByLabel('Username').fill(username)
  if (displayName) {
    await page.getByLabel('Display Name').fill(displayName)
  }
  if (email) {
    await page.getByLabel('Email').fill(email)
  }
  await page.getByLabel('New Password').fill(password)
  await page.getByLabel('Confirm Password').fill(password)
  await solveCaptchaViaUI(page)
  await page.getByRole('button', { name: 'Sign up' }).click()
}

/** Sign up with a passkey via the signup form (virtual authenticator must be enabled). */
export async function signUpWithPasskeyViaUI(
  page: Page,
  username: string,
  email: string,
  displayName = '',
) {
  await page.goto('/signup')
  await page.getByLabel('Username').fill(username)
  if (displayName) {
    await page.getByLabel('Display Name').fill(displayName)
  }
  await page.getByLabel('Email').fill(email)
  await page.getByRole('radio', { name: 'Passkey' }).click()
  await solveCaptchaViaUI(page)
  await page.getByRole('button', { name: 'Sign up with passkey' }).click()
  await expect(page).not.toHaveURL(/\/signup(?:\?.*)?$/)
}

/** Log in with a passkey via the login form (virtual authenticator must be enabled). */
export async function loginWithPasskeyViaUI(page: Page, username: string) {
  await page.goto('/login')
  await page.getByLabel('Username').fill(username)
  await page.getByLabel('Username').blur()
  const passkeyRadio = page.getByRole('radio', { name: 'Passkey' })
  if (await passkeyRadio.isVisible().catch(() => false))
    await passkeyRadio.click()
  await solveCaptchaViaUI(page)
  await page.getByRole('button', { name: /Sign in with passkey/i }).click()
  await expect(page).toHaveURL(/\/$/)
  await appMenuTrigger(page).waitFor({ state: 'visible' })
}

/**
 * Open Preferences on the given section and return the dialog locator, so
 * the dialog's accessible name is stated once. A spec that needs the dialog
 * after opening it at a section reads `const dialog = await
 * openSettingsAt(page, 'apps')` instead of re-deriving the role lookup at
 * every call site -- a rename of the dialog could not be half-applied.
 */
export async function openSettingsAt(page: Page, category?: string) {
  await openPreferencesDialog(page, category)
  return page.getByRole('dialog', { name: 'Preferences' })
}

/** Open Preferences on the account / profile section. */
export async function openAccountSettings(page: Page) {
  return openSettingsAt(page, 'account')
}

/**
 * Logout via the app menu (titlebar on desktop, tab-bar "+" menu on mobile).
 */
export async function logoutViaUI(page: Page) {
  await openAppMenu(page)
  await page.getByText('Log out').click()
  await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible()
}

/**
 * Open the context menu for a workspace item in the sidebar.
 * Finds the workspace by title text, then clicks the "..." menu trigger.
 */
export async function openWorkspaceContextMenu(page: Page, workspaceTitle: string) {
  const item = page.locator('[data-testid^="workspace-item-"]').filter({ hasText: workspaceTitle })
  // Hover to reveal the menu trigger (it may be hidden until hover)
  await item.hover()
  // Click the "..." button (DropdownMenu.Trigger inside the workspace item)
  await item.locator('button').first().click()
}

/**
 * Take a screenshot if E2E_SCREENSHOTS=1 is set.
 * Screenshots are saved to test-results/screenshots/{theme}/{name}.png
 */
export async function screenshotIfEnabled(page: Page, name: string) {
  if (process.env.E2E_SCREENSHOTS !== '1')
    return
  const theme = process.env.E2E_THEME || 'system'
  const dir = join('test-results', 'screenshots', theme)
  mkdirSync(dir, { recursive: true })
  await page.screenshot({ path: join(dir, `${name}.png`), fullPage: false })
}

/**
 * Browser preferences use the browserStorage wrapper with a value and expiry. The storage layer rejects missing or invalid wrappers.
 * Reading a raw field from the wrapper returns no preference. Writing an unwrapped object makes the app use defaults.
 * These helpers use the app registry for the key and lifetime, so storage-policy changes cannot invalidate fixture values unnoticed.
 */
function browserPrefsTtlMs(): number {
  const ttlMs = getTtlForKey(KEY_BROWSER_PREFS)
  if (ttlMs === null)
    throw new Error(`${KEY_BROWSER_PREFS} is missing from LOCAL_KEY_SPECS`)
  return ttlMs
}

const BROWSER_PREFS_TTL_MS = browserPrefsTtlMs()

/**
 * Read a single field from the consolidated browser preferences.
 *
 * Pass `leapmuxServer.adminUserId`. See `getBrowserPrefValue`.
 */
export async function getBrowserPref(page: Page, userId: string, field: string): Promise<string | null> {
  const value = await getBrowserPrefValue(page, userId, field)
  return value === null ? null : String(value)
}

/**
 * Read one browser preference without converting it to a string.
 * Structured values such as theme must remain objects. String conversion would make different objects compare as [object Object].
 * Use the supplied account ID and accountStorageKey. Scanning for a similar key could read another account after multiple logins.
 */
export async function getBrowserPrefValue(page: Page, userId: string, field: string): Promise<unknown> {
  const row = await readEntry(page, accountStorageKey(userId, KEY_BROWSER_PREFS))
  const prefs = row?.v
  if (prefs == null || typeof prefs !== 'object')
    return null
  const value = (prefs as Record<string, unknown>)[field]
  return value === undefined ? null : value
}

/**
 * Set one browser preference for the supplied account ID.
 * Pass leapmuxServer.adminUserId because the page did not sign in yet.
 * The page must already use the app origin. Await this write before reloading, so IndexedDB holds the value before the app reads it.
 * The value can be any supported preference type. For example, terminal size and opacity require numbers.
 * PreferencesContext rejects a numeric value stored as a string and uses the default instead.
 */
export async function setInitialBrowserPref(page: Page, userId: string, field: string, value: unknown) {
  const storedKey = accountStorageKey(userId, KEY_BROWSER_PREFS)
  const existing = await readEntry(page, storedKey)
  const prefs = (existing?.v != null && typeof existing.v === 'object')
    ? { ...existing.v as Record<string, unknown> }
    : {}
  prefs[field] = value
  await writeEntry(page, storedKey, prefs, Date.now() + BROWSER_PREFS_TTL_MS)
}

/** The hub's session cookie name, as `readSessionCookie` looks it up. */
const SESSION_COOKIE_NAME = 'leapmux-session'

/**
 * The session cookie the browser context currently holds, as the
 * "leapmux-session=<value>" token `loginViaToken` takes. Its inverse.
 *
 * Three specs hand-rolled this lookup. One home means a rename of the
 * cookie name moves every reader with it.
 */
export async function readSessionCookie(page: Page, step: string): Promise<string> {
  const session = (await page.context().cookies()).find(c => c.name === SESSION_COOKIE_NAME)
  if (!session?.value)
    throw new Error(`${step} did not set a session cookie on the browser context`)
  return `${SESSION_COOKIE_NAME}=${session.value}`
}

/**
 * Set the session cookie in the browser context so subsequent navigations
 * are authenticated. The token is a cookie string like "leapmux-session=<value>".
 * Must be called **before** any page.goto() calls.
 *
 * The NAME comes from the token, not from SESSION_COOKIE_NAME: the caller
 * passes back what a login handed it, whatever it was called.
 */
export async function loginViaToken(page: Page, token: string) {
  const [name, ...rest] = token.split('=')
  const value = rest.join('=')
  await page.context().addCookies([{
    name,
    value,
    domain: 'localhost',
    path: '/',
    httpOnly: true,
  }])
}

/**
 * Wait for the next layout save event. Uses a generation counter so the
 * event can fire before the returned promise is awaited without being lost.
 *
 * Usage:
 *   const saved = waitForLayoutSave(page)
 *   await doSomethingThatTriggersLayoutSave()
 *   await saved
 */
export function waitForLayoutSave(page: Page): Promise<void> {
  // Capture the current generation and install a one-shot listener that
  // resolves a promise on the next event. The generation counter guards
  // against the event firing between the evaluate call and the listener
  // being attached (the counter is incremented by a persistent listener
  // installed once per page).
  return page.evaluate(() => {
    const w = window as any
    if (w.__layoutSaveGenInstalled == null) {
      w.__layoutSaveGen = 0
      window.addEventListener('leapmux:layout-saved', () => {
        w.__layoutSaveGen++
      })
      w.__layoutSaveGenInstalled = true
    }
    const genBefore = w.__layoutSaveGen as number
    return new Promise<void>((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error('layout save timeout')), 30_000)
      const check = () => {
        if ((w.__layoutSaveGen as number) > genBefore) {
          clearTimeout(timer)
          resolve()
        }
      }
      window.addEventListener('leapmux:layout-saved', () => {
        check()
      }, { once: true })
      // Also check immediately in case it fired between genBefore read and listener attach.
      check()
    })
  })
}

/** The composer's status bar. Use {@link settingsChips} to read its values. */
export function settingsBar(page: Page) {
  return page.locator('[data-testid="composer-status-bar"]')
}

/**
 * Locate the visible status-bar chip triggers.
 * Assert on these triggers instead of the entire bar. Closed sibling popovers retain every option label in the DOM.
 * A bar-level text assertion could therefore pass for an option that the user did not select.
 */
export function settingsChips(page: Page) {
  return page.locator('[data-testid="composer-status-bar"] [data-testid$="-trigger"]')
}

/** Assert that some status-bar chip displays `text`. */
export async function expectSettingsChip(page: Page, text: string | RegExp) {
  await expect(settingsChips(page).filter({ hasText: text })).not.toHaveCount(0)
}

/** Assert that NO status-bar chip displays `text`. */
export async function expectNoSettingsChip(page: Page, text: string | RegExp) {
  await expect(settingsChips(page).filter({ hasText: text })).toHaveCount(0)
}

/**
 * The option-group id encoded in an option's test id.
 *
 * `OptionGroupMenuItems` emits `<groupId>-<value>` for every option. A group id
 * never contains a hyphen while a value can ("danger-full-access"), so the
 * split is on the FIRST hyphen.
 */
function settingsGroupIdOf(optionTestId: string): string {
  const i = optionTestId.indexOf('-')
  if (i <= 0)
    throw new Error(`settings option test id must be "<groupId>-<value>", got "${optionTestId}"`)
  return optionTestId.slice(0, i)
}

/**
 * Close composer popovers before another menu interaction. An open submenu can cover another group trigger.
 * Use hidePopover because Escape affects only the popover that holds focus, which the caller cannot reliably identify.
 */
export async function closeComposerMenus(page: Page) {
  await page.evaluate(() => {
    for (const el of document.querySelectorAll<HTMLElement>('[popover]')) {
      if (el.matches(':popover-open'))
        el.hidePopover()
    }
  })
  await expect(page.locator('[data-testid="composer-plus-trigger"]')).toHaveAttribute('aria-expanded', 'false')
}

/**
 * Click the trigger unless aria-expanded already reports an open popover. Then confirm aria-expanded.
 * A closing popover stays visible during its animation. A visibility check alone could incorrectly skip the click.
 * Call this idempotent operation inside toPass so an app update that closes the menu permits another attempt.
 */
async function ensureExpanded(trigger: Locator) {
  if (await trigger.getAttribute('aria-expanded') !== 'true')
    await trigger.click()
  await expect(trigger).toHaveAttribute('aria-expanded', 'true')
}

/**
 * Whether the agent's permission-mode picker currently offers one option id.
 *
 * A permission shortcut is drawn only when the session offers every value its preset
 * sets, so a spec that must know which shortcut to expect asks the picker rather than
 * guessing from the provider. Leaves every composer menu closed.
 */
export async function permissionModeOffered(page: Page, modeId: string): Promise<boolean> {
  const group = await openSettingsMenu(page, 'permissionMode')
  const offered = await group.locator(`[data-testid="permissionMode-${modeId}"]`).count() > 0
  await closeComposerMenus(page)
  return offered
}

/** Apply one composer permission preset and wait for the settings round-trip. */
export async function applyPermissionPreset(page: Page, kind: 'smart' | 'bypass') {
  const menu = await openPlusMenu(page)
  await menu.getByTestId(`composer-${kind}-permissions`).click()
  await waitForSettingsIdle(page)
}

/** Open the composer's `[+]` menu and leave it open. */
export async function openPlusMenu(page: Page): Promise<Locator> {
  const plus = page.locator('[data-testid="composer-plus-trigger"]')
  await expect(plus).toBeVisible()
  await closeComposerMenus(page)
  await expect(async () => {
    await ensureExpanded(plus)
  }).toPass()
  return page.locator('[data-testid="composer-plus-popover"]')
}

/**
 * Locate an option-group trigger in the open plus menu.
 * The trigger exists only when the agent offers the group. Its absence indicates that the option group does not apply.
 */
export function settingsGroupTrigger(page: Page, groupId: string): Locator {
  return page.locator(`[data-testid="composer-group-${groupId}"]`)
}

/**
 * Open an option-group submenu through the composer plus menu and leave it open.
 * The plus menu includes every group at all widths. Status-bar chips expose only some groups and can be hidden.
 * Open both menus within the same toPass attempt because a settings response can close the first before the second opens.
 * Close existing menus inside each attempt. Otherwise, a menu with outdated content could remain open through every retry.
 */
export async function openSettingsMenu(page: Page, groupId: string): Promise<Locator> {
  const plus = page.locator('[data-testid="composer-plus-trigger"]')
  const submenu = settingsGroupTrigger(page, groupId)
  await expect(plus).toBeVisible()
  await expect(async () => {
    // Start closed: a submenu left open from a previous group covers this one's
    // trigger, and the click below would be intercepted.
    await closeComposerMenus(page)
    await ensureExpanded(plus)
    await ensureExpanded(submenu)
  }).toPass()
  // Scope every option lookup to THIS popover. The status-bar chip renders the
  // same group with the same per-option test ids, so an unscoped locator
  // resolves to two elements and fails Playwright's strict-mode check.
  return page.locator(`[data-testid="composer-group-${groupId}-popover"]`)
}

/**
 * Pick `testId` out of the agent settings menu, opening its group first.
 *
 * Open-then-click is retried as ONE unit: a settings round-trip landing
 * between the two re-renders the dropdown and can close it, and an open that
 * has already been awaited cannot be re-established by the click itself. The
 * caller's invariant is "this option got chosen", so that is what is retried.
 */
export async function chooseSettingsOption(page: Page, testId: string) {
  await expect(async () => {
    const menu = await openSettingsMenu(page, settingsGroupIdOf(testId))
    await menu.locator(`[data-testid="${testId}"]`).click()
  }).toPass()
}

/**
 * Locate a directory-tree row by its displayed name and row test ID.
 * A Tooltip duplicates truncated text, so a text-only locator can match twice.
 * Apply :visible before first() because the sidebar has another mounted copy. The other copy can intercept or reject pointer actions.
 */
export function treeRow(page: Page, name: string): Locator {
  return page.locator(`[data-testid="tree-row"]${VISIBLE}`).filter({ hasText: name }).first()
}

/**
 * Every visible tree row's NAME, in display order — for asserting on the sort.
 *
 * A row's own text is not its name: the three-dot menu renders inside the row
 * and stays mounted while closed, so the row carries its menu items' text too.
 */
export function treeRowNames(page: Page): Locator {
  return page.locator(`[data-testid="tree-row"]${VISIBLE} [data-testid="tree-row-name"]`)
}

/**
 * Wait until the draft reaches durable browser storage before reload.
 * The debounce and write queue can both delay persistence. A fixed sleep cannot account for delayed timers on a busy host.
 * The caller lacks the agent ID, so inspect draft rows for its account.
 * Build the prefix with accountStorageKey and compare with startsWith. A regular expression would interpret metacharacters in the key.
 */
export async function waitForEditorDraft(page: Page, userId: string, text: string) {
  const prefix = accountStorageKey(userId, PREFIX_EDITOR_DRAFT)
  await expect.poll(async () => {
    for (const key of await storageKeys(page)) {
      if (!key.startsWith(prefix))
        continue
      const row = await readEntry(page, key)
      const content = (row?.v as { content?: unknown } | undefined)?.content
      if (typeof content === 'string' && content.includes(text))
        return true
    }
    return false
  }, `the editor draft "${text}" must be persisted before the reload`).toBe(true)
}

/**
 * Wait until the Files sort preference reaches durable browser storage.
 * The write queue can outlive the click. pagehide flush also awaits a connection, so it does not complete synchronously.
 * Match the prefix, worker ID, and stored value. The worker can canonicalize the directory path, such as /var to /private/var on macOS.
 * One agent per test makes this lookup unambiguous.
 */
export async function waitForFilesSortOrder(
  page: Page,
  userId: string,
  workerId: string,
  expected: FileSortOrder,
) {
  const prefix = accountStorageKey(userId, `${PREFIX_FILES_SORT_ORDER}${workerId}:`)
  await expect.poll(async () => {
    for (const key of await storageKeys(page)) {
      if (!key.startsWith(prefix))
        continue
      const row = await readEntry(page, key)
      const stored = row?.v as { key?: unknown, direction?: unknown } | undefined
      if (stored?.key === expected.key && stored?.direction === expected.direction)
        return true
    }
    return false
  }, `the sort order ${expected.key}/${expected.direction} must be persisted before the reload`).toBe(true)
}

/**
 * Rename a tab through its inline editor and wait for the label.
 * A rename writes a terminal title to storage. Terminal creation supplies the initial Terminal <Name> value.
 * Terminal escape-sequence titles are live overlays and do not persist. See SignalTitle in the worker terminal.go file.
 */
export async function renameTabViaUI(page: Page, tab: Locator, newTitle: string) {
  await tab.dblclick()
  const input = page.locator(`[data-testid="tab-rename-input"]${VISIBLE}`)
  await expect(input).toBeVisible()
  await input.fill(newTitle)
  await input.press('Enter')
  await expect(tab).toContainText(newTitle)
}

/**
 * Open a sidebar row menu and wait for the requested item.
 * Retry hover, trigger click, and item lookup together. Git refreshes and turn completion can replace a row during the interaction.
 * Workspace, worker, and todo changes can also update the sidebar. An open-menu check alone cannot ensure that the requested item remains.
 * File-tree and branch-group menus share this helper and supply their own trigger locators.
 */
export async function openRowMenu(row: Locator | null, trigger: Locator, item: Locator) {
  await expect(async () => {
    if (!await item.isVisible()) {
      // A row menu's trigger is `opacity: 0` until its row is hovered. A
      // SECTION HEADER's trigger is always painted, so that caller passes
      // `null` rather than a row to hover.
      await row?.hover()
      await trigger.click()
    }
    await expect(item).toBeVisible()
  }).toPass()
}

/**
 * Open a row's menu and click one of its items, retried together.
 *
 * Same reasoning as {@link openRowMenu}: the menu can vanish between opening it
 * and clicking, so the caller's invariant -- "this item got clicked" -- is what
 * gets retried.
 */
export async function clickRowMenuItem(row: Locator | null, trigger: Locator, item: Locator) {
  await expect(async () => {
    await openRowMenu(row, trigger, item)
    await item.click()
  }).toPass()
}

/** The three-dot trigger inside a file-tree row. */
function treeMenuTrigger(row: Locator): Locator {
  return row.locator('[data-testid="tree-context-button"]')
}

/**
 * Open a tree row's context menu, with `requiredItem` on screen.
 *
 * `requiredItem` defaults to the one entry every variant of the menu carries,
 * for callers that only need it open.
 */
export async function openTreeContextMenu(page: Page, row: Locator, requiredItem = 'tree-copy-path-button') {
  await openRowMenu(row, treeMenuTrigger(row), page.locator(`[data-testid="${requiredItem}"]:visible`))
}

/** Open a tree row's context menu and click one of its items. */
export async function clickTreeContextItem(page: Page, row: Locator, itemTestId: string) {
  await clickRowMenuItem(row, treeMenuTrigger(row), page.locator(`[data-testid="${itemTestId}"]:visible`))
}

/**
 * Locate the first visible branch-group row.
 * The sidebar has two mounted copies. An unfiltered first() can select a covered copy that rejects pointer actions.
 * Use aria-expanded to identify the menu trigger. A last-button lookup can instead select a hidden menu item inside the popover.
 */
export function branchGroupRow(page: Page): Locator {
  return page.locator(`[data-testid="tab-tree-branch-group"]${VISIBLE}`).first()
}

function branchMenuTrigger(row: Locator): Locator {
  return row.locator('[aria-expanded]').first()
}

/**
 * Locate the first visible repository-group header below root.
 * Visibility filtering excludes the covered sidebar copy.
 * Supply the workspace children as root when another workspace can also remain expanded. A page-level lookup selects the first workspace.
 * Branch rows are siblings of the header, so a lookup below this row reaches only the repository menu.
 */
export function repoGroupRow(root: Page | Locator): Locator {
  return root.locator(`[data-testid="tab-tree-repo-group"]${VISIBLE}`).first()
}

/** The repository row's three-dot trigger. Only the trigger carries `aria-expanded`. */
function repoMenuTrigger(row: Locator): Locator {
  return row.locator('[data-testid="repo-row-menu-trigger"]')
}

/**
 * Open a repository group's three-dot menu, with `requiredItem` on screen.
 *
 * The item is looked up INSIDE the row, not on the page: `DropdownMenu` renders
 * its children eagerly, so every other repository row on screen holds a hidden
 * copy of the same item and a page-rooted `getByRole` resolves to several.
 */
export async function openRepoMenu(row: Locator, requiredItem = 'Collapse all branches') {
  await openRowMenu(row, repoMenuTrigger(row), repoMenuItem(row, requiredItem))
}

/** Open a repository group's three-dot menu and click one of its items. */
export async function clickRepoMenuItem(row: Locator, itemName: string) {
  await clickRowMenuItem(row, repoMenuTrigger(row), repoMenuItem(row, itemName))
}

/**
 * One item of a repository row's menu, by its exact name.
 *
 * `exact`, because Playwright matches an accessible name by substring
 * otherwise -- and this menu carries `Copy repository URL` beside
 * `Copy repository path`, so a loose `Copy repository` matches both.
 */
export function repoMenuItem(row: Locator, name: string): Locator {
  return row.getByRole('menuitem', { name, exact: true })
}

/**
 * Open a branch group's three-dot menu, with `requiredItem` on screen.
 *
 * Reopening the SAME menu right after a dialog closes needs a wait in between:
 * the menu stays open behind a modal dialog, so `openRowMenu` sees the item as
 * visible, skips the trigger, and then clicks an item that disappears with the
 * dialog. Wait for `menu[popover]:visible` to reach 0 first.
 */
export async function openBranchMenu(page: Page, row: Locator, requiredItem = 'Switch to branch...') {
  await openRowMenu(row, branchMenuTrigger(row), page.getByRole('menuitem', { name: requiredItem }))
}

/** Open a branch group's three-dot menu and click one of its items. */
export async function clickBranchMenuItem(page: Page, row: Locator, itemName: string) {
  await clickRowMenuItem(row, branchMenuTrigger(row), page.getByRole('menuitem', { name: itemName }))
}

/**
 * Wait for the in-flight settings indicator to clear.
 *
 * The marker rides the composer's always-present `[+]` trigger, NOT the status
 * bar: the bar is a preference that menu can switch off, which would otherwise
 * take the only in-flight feedback with it.
 */
export async function waitForSettingsIdle(page: Page) {
  await expect(page.locator('[data-testid="settings-loading-spinner"]')).not.toBeVisible()
}

/**
 * Wait for an option catalog through the model submenu.
 * Status-bar chips can be hidden by a preference. The plus menu remains available.
 * A submenu exists only when the agent supplies a group with at least one option.
 * Close and reopen the menu on each attempt so a previous empty menu cannot hide newly supplied options.
 */
export async function waitForSettingsHydrated(page: Page) {
  const plus = page.locator('[data-testid="composer-plus-trigger"]')
  await expect(plus).toBeVisible()
  await expect(async () => {
    await closeComposerMenus(page)
    await ensureExpanded(plus)
    await expect(settingsGroupTrigger(page, 'model')).toBeVisible()
  }).toPass()
  await closeComposerMenus(page)
}

/**
 * Wait for a workspace page to be fully loaded.
 * Waits for either a tab or the empty tile actions/hint.
 *
 * Each locator uses `.first()` because workspaces can have multiple
 * tabs visible; without `.first()`, Playwright's strict-mode check
 * throws on multi-match — `isMaybeVisible` swallows the error and
 * returns false, masking that the workspace IS ready.
 *
 * `timeoutMs` is forwarded to the underlying `expect.poll` so callers
 * driving the dev-mode worker (where worker subprocess spawn extends
 * first-render latency beyond the default expect timeout) can extend
 * the wait without re-implementing the readiness shape.
 */
export async function waitForWorkspaceReady(page: Page, timeoutMs?: number) {
  const pollOpts = timeoutMs != null ? { timeout: timeoutMs } : undefined
  await expect.poll(async () => {
    if (await page.locator('[data-testid="tab"]').first().isVisible().catch(() => false))
      return true
    // Mobile layout: there is no tab strip, and a workspace that HAS tabs
    // never shows the empty-tile placeholder either — the current-tab chip
    // is the one signal that the workspace shell rendered with its tabs.
    if (await page.locator('[data-testid="tab-chip"]').first().isVisible().catch(() => false))
      return true
    if (await page.locator('[data-testid="empty-tile-actions"]').first().isVisible().catch(() => false))
      return true
    if (await page.locator('[data-testid="empty-tile-hint"]').first().isVisible().catch(() => false))
      return true
    return false
  }, pollOpts).toBe(true)
}

/**
 * Locate the visible sidebar row for workspaceId.
 * Desktop and mobile sidebars mount separate copies of the same workspace ID.
 * Filter visibility before resolving the locator, or Playwright can reject both matches before its wait begins.
 * Use first() because both copies can briefly remain visible during a layout transition.
 * Either visible copy identifies the same workspace.
 */
export function workspaceRow(page: Page, workspaceId: string): Locator {
  return page.locator(`[data-testid="workspace-item-${workspaceId}"]${VISIBLE}`).first()
}

/**
 * Locate the workspace chevron through its test ID and visible workspace row.
 * The drag grip precedes it in SVG order, so a first-SVG lookup is incorrect.
 * An independent chevron lookup can select the hidden collapsed sidebar copy and wait for a click that cannot succeed.
 */
export function workspaceChevron(page: Page, workspaceId: string): Locator {
  return workspaceRow(page, workspaceId)
    .locator(`[data-testid="workspace-chevron-${workspaceId}"]`)
}

/**
 * Locate tab leaves below the visible workspace row.
 * The other mounted sidebar copy can retain unhydrated labels, so a page query can read a permanent generic Agent label.
 * The children wrapper follows the workspace row as a sibling. Use that sibling relationship for the lookup.
 */
export function sidebarLeaves(page: Page, workspaceId: string): Locator {
  return workspaceRow(page, workspaceId)
    .locator('xpath=following-sibling::*[1]')
    .locator('[data-testid="tab-tree-leaf"]')
}

/**
 * Rendered titles of `workspaceId`'s sidebar leaves, with the close icon /
 * badges stripped so only the label text remains.
 */
export async function sidebarLeafLabels(page: Page, workspaceId: string): Promise<string[]> {
  return sidebarLeaves(page, workspaceId).evaluateAll(leaves =>
    leaves.map((leaf) => {
      const clone = leaf.cloneNode(true) as HTMLElement
      clone.querySelectorAll('button, svg').forEach(n => n.remove())
      return (clone.textContent ?? '').trim()
    }),
  )
}

/**
 * Tab ids of `workspaceId`'s sidebar leaves.
 *
 * Ids, not rendered titles: a title is Worker-sourced metadata, so with the
 * Worker offline every row falls back to the generic "Agent" label. That
 * fallback is correct behaviour and says nothing about where the tab lives.
 */
export async function sidebarLeafIds(page: Page, workspaceId: string): Promise<string[]> {
  return sidebarLeaves(page, workspaceId)
    .evaluateAll(leaves => leaves.map(leaf => leaf.getAttribute('data-tab-id') ?? ''))
}

/**
 * Load the app, select workspaceId through its sidebar row, and wait for its shell.
 * The app uses / for every workspace. resolveActiveWorkspace reads the saved selection from browser storage.
 * A click uses the same selection path as the user. Skip it when the requested workspace is already active.
 *
 * A cold load can first activate another saved workspace. That activation expands its sidebar row and hydrates its tabs.
 * Tests of expansion state must establish their starting state explicitly. See the expanded-state test in 017.
 */
export async function openWorkspace(page: Page, workspaceId: string) {
  await page.goto('/')
  const row = workspaceRow(page, workspaceId)
  await row.waitFor()
  if (await row.getAttribute('data-active') !== 'true') {
    // On the mobile layout the rows live in a drawer that starts closed;
    // open it so the click can land (selecting a workspace closes the
    // drawers again). `isVisible` on the off-screen drawer content would
    // also pass, which is why the row wait alone cannot tell.
    const toggle = page.getByRole('button', { name: 'Toggle workspaces' })
    if (await toggle.isVisible().catch(() => false))
      await toggle.click()
    await row.click()
  }
  await expect(row).toHaveAttribute('data-active', 'true')
  await waitForWorkspaceReady(page)
}

/**
 * Reload / and verify that the app restores workspaceId from saved state.
 * Do not click the sidebar row. That click would hide a broken restore.
 * Use openWorkspace only when explicit selection is intended.
 */
export async function reopenWorkspace(page: Page, workspaceId: string) {
  await page.goto('/')
  await expect(workspaceRow(page, workspaceId)).toHaveAttribute('data-active', 'true')
  await waitForWorkspaceReady(page)
}

/**
 * Authenticate as `token`, open `workspaceId`, and wait for the workspace shell
 * to be ready (first tile rendered). Shared across the multi-context
 * CRDT-convergence specs (150/151/152/153) that all need the same setup before
 * driving layout mutations.
 */
export async function gotoWorkspace(page: Page, token: string, workspaceId: string) {
  await loginViaToken(page, token)
  await openWorkspace(page, workspaceId)
  // Wait for the bootstrap event so subsequent mutations reach the
  // store via the WS round-trip rather than the fallback projection.
  await page.locator('[data-testid="tile"]').first().waitFor()
}

/**
 * Read the rendered titles of every agent tab in the tabbar, stripping
 * the close / notification / remote-badge child nodes so the returned
 * text matches the visible label. Used by specs that assert dragged or
 * restored tabs keep their metadata.
 */
export async function tabbarAgentLabels(page: Page): Promise<string[]> {
  return page.locator('[data-testid="tab"][data-tab-type="agent"]').evaluateAll(els =>
    els.map((el) => {
      const clone = el.cloneNode(true) as HTMLElement
      // A row's context menu keeps its items in the DOM behind the popover
      // attribute while closed; they are not part of the label. Strip every
      // popover alongside the close button and the badges.
      clone.querySelectorAll('[data-testid="tab-close"], [data-testid="tab-notification"], [data-testid="tab-remote-badge"], [popover]').forEach(n => n.remove())
      return (clone.textContent ?? '').trim()
    }),
  )
}

/** Locate a tab by its hub-side `tab_id`. */
export function tabById(page: Page, tabId: string): Locator {
  return page.locator(`[data-testid="tab"][data-tab-id="${tabId}"]`)
}

/**
 * Bounding box of `locator`, after waiting for it to be visible.
 *
 * `boundingBox()` returns null for an element that is not laid out yet, and a
 * bare `expect(...).toHaveCount(n)` beforehand does NOT guarantee layout — it
 * settles the count, not the paint. Every drag test needs a real box to compute
 * pointer coordinates from, so a null there surfaces as "Could not get bounding
 * boxes" with nothing else wrong. Waiting first removes the race.
 */
export async function boxOf(locator: Locator): Promise<{ x: number, y: number, width: number, height: number }> {
  // Return the geometry captured inside the poll.
  // A separate read could follow a workspace switch that replaces the tile and tab strip.
  // expect.poll retries through the global assertion timeout.
  let box: { x: number, y: number, width: number, height: number } | null = null
  await expect.poll(async () => {
    box = await locator.boundingBox()
    return box !== null
  }).toBe(true)
  if (!box)
    throw new Error(`No bounding box for ${locator}`)
  return box
}

/**
 * Pick an option from a `DropdownMenu`.
 *
 * The app renders no native `<select>` any more (see the dropdown rule in
 * CLAUDE.md), so `selectOption` has nothing to drive. A menu keeps its items
 * mounted, so the click has to be scoped to the row that owns them.
 */
// The testids used here are WRITTEN by `~/components/common/LoadingMenu`, and
// the Vitest counterpart in `src/test-support/menu.ts` encodes the same two
// templates. The two cannot share query code -- one drives the DOM, the other a
// Playwright Locator -- so a rename has to be applied in all three files.
export async function pickMenuOption(scope: Locator, base: string, value: string): Promise<void> {
  await openMenu(scope, base)
  await scope.getByTestId(base).getByTestId(`loading-menu-option-${value}`).first().click()
}

/**
 * Open a `LoadingMenu` if it is not already open.
 *
 * IDEMPOTENT on purpose. A caller that reads the options and then picks one
 * would otherwise toggle the menu shut between the two, and the click would
 * land on an item hidden from the accessibility tree.
 */
export async function openMenu(scope: Locator, base: string): Promise<void> {
  const trigger = scope.getByTestId(`${base}-trigger`)
  if (await trigger.getAttribute('aria-expanded') !== 'true')
    await trigger.click()
}

/**
 * The colour `value` resolves to, as the browser computes it.
 *
 * `getPropertyValue('--code-block-background')` answers with the SPECIFIED
 * token, which for a derived field is the `color-mix()` expression itself and
 * never changes with the theme. Assigning the value to a probe element and
 * reading `background-color` back gives the resolved `rgb(...)`, in the same
 * normalized form `getComputedStyle` reports for a real element -- so a token
 * and an element can be compared to each other.
 */
export async function resolvedColor(page: Page, value: string): Promise<string> {
  return page.evaluate((css) => {
    const probe = document.createElement('div')
    probe.style.backgroundColor = css
    document.body.append(probe)
    const painted = getComputedStyle(probe).backgroundColor
    probe.remove()
    return painted
  }, value)
}

/** The theme chooser's palette menu, whose options are keyed by theme id. */
export async function pickTheme(scope: Locator, themeId: string): Promise<void> {
  await scope.getByTestId('theme-chooser-name').click()
  await scope.getByTestId(`theme-option-${themeId}`).click()
}

/** The theme chooser's variant menu, keyed by variant id. */
export async function pickThemeVariant(scope: Locator, variantId: string): Promise<void> {
  await scope.getByTestId('theme-chooser-variant').click()
  await scope.getByTestId(`variant-option-${variantId}`).click()
}

/**
 * The option labels a `LoadingMenu` offers, opening it first.
 *
 * A closed popover is hidden from the accessibility tree, so a role query
 * against it matches nothing — indistinguishable from "the list is empty" and,
 * under `expect`, from a hang. The `<select>` this replaced needed no open:
 * every `<option>` was readable from the collapsed control.
 */
export async function menuOptionTexts(scope: Locator, base: string): Promise<string[]> {
  await openMenu(scope, base)
  return scope.getByTestId(base).getByRole('menuitemradio').allTextContents()
}

/**
 * One option row's LABEL, which is not the same as its text.
 *
 * A row also carries its DETAIL -- the age of a session -- and an age is a
 * moving target: read the whole row and an assertion can lose to the clock
 * between the read and the compare. `DropdownMenuCheckableItem` derives this
 * test id from the row's own, and `src/test-support/menu.ts` spells the same
 * suffix for the vitest side.
 */
export function menuOptionLabel(row: Locator): Locator {
  return row.locator('[data-testid$="-label"]')
}
