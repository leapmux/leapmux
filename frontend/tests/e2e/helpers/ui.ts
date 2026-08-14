import type { Locator, Page } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'

import { expect } from '@playwright/test'
import { EXACT_KEY_TTLS, KEY_BROWSER_PREFS, PREFIX_EDITOR_DRAFT } from '../../../src/lib/browserStorage'

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
 * Assert that a sidebar label declares the one-line clip.
 *
 * Only a real browser resolves this. The rules arrive through a COMPOSED
 * vanilla-extract style (`style([clippedText, …])`), so the element carries two
 * class names and the declarations come from two rules; jsdom loads no
 * stylesheet at all, so a unit test can see the class but never the outcome.
 *
 * `min-width` is asserted with the rest, because it is the one that decides
 * whether the ellipsis ever fires: a flex item defaults to `min-width: auto`
 * and keeps the width of its own text, so the other three declarations sit
 * there and do nothing. Several labels shipped exactly that way.
 *
 * This states what the label DECLARES. Pair it with {@link expectClipsLongText},
 * which measures what the label DOES.
 *
 * Pass the LABEL, not an ancestor. A clipped label usually sits inside
 * `Tooltip`, which wraps its child in a bare `display: contents` span; that
 * wrapper holds the same text and comes first, so a loose `span` locator
 * resolves to it and reports `text-overflow: clip`.
 */
export async function expectClipsToOneLine(label: Locator) {
  await expect(label).toHaveCSS('white-space', 'nowrap')
  await expect(label).toHaveCSS('text-overflow', 'ellipsis')
  await expect(label).toHaveCSS('overflow-x', 'hidden')
  await expect(label).toHaveCSS('min-width', '0px')
}

/**
 * Assert that a LONG label clips itself and widens no scroller above it.
 *
 * The declarations alone prove nothing. A worker name already declared the
 * whole quartet while the ellipsis still never fired, because the list's
 * container sized to its widest row; only a label longer than its box separates
 * the two containers. No fixture can give the dev worker a long name, so this
 * writes one into the text node and restores it inside ONE synchronous block.
 * The write forces the reflow that the reads below need, and Solid keeps its
 * reference to the text node because the node itself is reused.
 *
 * A scroller is a box whose computed `overflow-x` is `auto` or `scroll`.
 * Testing that is what separates "an ancestor grew a scrollbar" from "the label
 * clips its own text" -- a clipped label reports `scrollWidth > clientWidth` BY
 * CONSTRUCTION, which is the same measurement the ellipsis is made of.
 *
 * A 1px tolerance absorbs sub-pixel layout rounding, which reports a scrollable
 * width larger than the client width on a box that shows no scrollbar.
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
 * Send a message via the ProseMirror editor.
 *
 * Types at the default (zero) inter-key delay. The 100ms-per-key delay this
 * used to carry bought nothing -- every key event is still dispatched in
 * order, and ProseMirror handles them synchronously -- but it cost ~5s on the
 * shared arithmetic prompt alone, on every one of the ~60 sends in the suite.
 * The pagination and scroll-rail specs had already been typing without it.
 *
 * Specs that exercise ProseMirror's own input rules (markdown shortcuts,
 * mention/slash triggers) keep their local, deliberately paced typing.
 */
export async function sendMessage(page: Page, text: string) {
  const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
  await expect(editor).toBeVisible()
  await editor.click()
  await page.keyboard.type(text)
  await page.keyboard.press('Meta+Enter')
  // The editor emptying is the app's acknowledgement that the send committed.
  // Waiting on it here stops a caller from racing ahead of its own message --
  // which matters more now that typing no longer takes seconds.
  await expect(editor).toHaveText('')
}

/** Wait for the control request banner to appear and return a scoped locator. */
export async function waitForControlBanner(page: Page) {
  const banner = page.locator('[data-testid="control-banner"]')
  await expect(banner).toBeVisible()
  return banner
}

/**
 * Every chat row exists in the DOM TWICE for as long as its height is unknown.
 *
 * ChatView mounts a faithful copy of each unmeasured row inside its hidden
 * premeasure root (`ChatHiddenPremeasure`) purely to read a height off it: same
 * test ids, same text, same classes, `visibility: hidden`. Separately, a real
 * row that is waiting on its own measurement is itself hidden in place. So a
 * bare `[data-testid="message-bubble"]` transiently resolves to two elements
 * per message, and Playwright's strict mode fails the assertion outright --
 * `strict mode violation: ... resolved to 2 elements`, with both matches
 * carrying identical markup.
 *
 * A probe sampling the DOM every 20ms saw 6 bubbles for 2 messages, 4 of them
 * hidden, in 1 of 596 samples on an idle machine. That is the whole story
 * behind this suite's "only flaky at high worker counts" chat failures: load
 * widens the measurement window, it does not create the bug.
 *
 * `:visible` is the fix and it belongs on the OUTERMOST chat locator only --
 * anything scoped under an already-visible bubble cannot be in the premeasure
 * root, so descendants need no filter.
 *
 * Prefer the helpers below to hand-written chat selectors.
 */
const VISIBLE = ':visible'

/**
 * CSS selector for agent message bubbles, WITHOUT the visibility filter.
 *
 * Safe only when scoped under an element already known to be visible (e.g.
 * `firstAssistantMessageRow(page).locator(ASSISTANT_BUBBLE_SELECTOR)`), or as
 * the `has:` argument of a filter, which resolves relative to the outer match.
 * Rooted at the page it matches the premeasure copy too -- use
 * {@link assistantBubbles}.
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
 * Return a locator for the visible band ROWS -- the full-bleed strips an
 * assistant message (`data-band="text"`) and a thought (`data-band="thought"`)
 * paint behind themselves. Pass a kind to restrict to one of the two.
 *
 * The strip is the row's own background and border, so it is the ROW that
 * carries the marker, not the bubble inside it. Every style class is a hashed
 * vanilla-extract name, which is why the attribute exists at all.
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
 * Measure a full-bleed chat element against the width it must span: the chat
 * scroll container's `clientWidth`, which IS its padding box (the scrollbar
 * excluded), and which is exactly what a band or a turn-end rule reaches.
 *
 * The container is resolved from the element itself rather than from a
 * hard-coded position in the DOM, and by the attribute the app publishes rather
 * than by a computed-style walk that has to guess which ancestor scrolls. Both
 * numbers are read in one browser round trip, so they cannot straddle a resize.
 */
export async function measureAgainstChatList(locator: Locator): Promise<{ width: number, listWidth: number }> {
  return locator.evaluate((el, selector) => {
    const list = el.closest(selector)
    if (!list)
      throw new Error('measureAgainstChatList: element is not inside the chat scroll container')
    return { width: el.getBoundingClientRect().width, listWidth: list.clientWidth }
  }, CHAT_SCROLL_CONTAINER)
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
 * The row hosting the first agent bubble that is a real assistant MESSAGE --
 * one carrying the per-message actions (quote, copy) on its row.
 *
 * Not every agent-role bubble is a message: turn-end dividers and the notices
 * an agent emits around startup render through the same bubble component with
 * no onReply, so they have neither a quote affordance nor prose to select.
 * firstAssistantBubble() takes whichever is first in the DOM, so a spec that
 * then reaches for the reply button or drags across the text can bind to one of
 * those and spend its whole timeout waiting for an element that will never
 * appear there. Whether a notice lands ahead of the reply depends on how busy
 * the machine is, which is why this only bites some runs.
 *
 * Specs about quoting or selecting an assistant message should name the message
 * rather than take the first bubble and hope.
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
 * A SECOND arithmetic probe, for specs that need a follow-up turn whose answer
 * is distinguishable from the first. 3333 is not a substring of 6912 and vice
 * versa, so a wait for either cannot be satisfied by the other turn's leftover
 * bubble -- which is the whole reason these two numbers, and not any others.
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
 * How long to give the thinking indicator to appear after a send. Expiring is
 * an expected outcome (see waitForAgentIdle), so this is a probe budget rather
 * than a deadline: long enough to catch a normal turn starting, short enough
 * that a turn which already finished does not pay for the wait.
 */
const APPEARANCE_PROBE_MS = 2000

/** Wait for the agent to finish its current turn (thinking indicator gone). */
export async function waitForAgentIdle(page: Page, timeoutMs = 120_000) {
  const thinking = page.locator('[data-testid="thinking-indicator"]')
  // The indicator has to be given a chance to APPEAR first: asserting it is
  // absent the instant after a send would pass against a turn that has not
  // started yet. Waiting for the appearance rather than sleeping a flat 2s
  // returns as soon as it shows, usually within tens of ms.
  //
  // The wait EXPIRING is a normal outcome, not a failure: a turn that finished
  // before we looked never shows an indicator at all, which is why the
  // rejection is swallowed. That is also why the budget is explicit rather than
  // inherited -- `locator.waitFor` takes `use.actionTimeout` (30s), and paying
  // that on every already-finished turn would cost more than the flat sleep
  // this replaced.
  //
  // What the budget does NOT do is close the slow-machine gap: an indicator
  // that first paints after it still reads as idle, exactly as it did under the
  // sleep. It is the same bound, spent only when it has to be.
  await thinking.waitFor({ state: 'visible', timeout: APPEARANCE_PROBE_MS }).catch(() => {})
  await expect(thinking).not.toBeVisible({ timeout: timeoutMs })
}

// ──────────────────────────────────────────────
// UI helpers
// ──────────────────────────────────────────────

/** Open the Preferences dialog from the app menu. */
export async function openPreferencesDialog(page: Page) {
  await page.getByTestId('app-menu-trigger').first().click()
  await page.getByRole('menuitem', { name: 'Preferences' }).click()
  await expect(page.getByRole('dialog', { name: 'Preferences' })).toBeVisible()
}

/**
 * Login via the UI form. Navigates to /login, fills credentials,
 * and waits for redirect to the app home.
 */
export async function loginViaUI(page: Page, username = 'admin', password = 'admin123') {
  await page.goto('/login')
  await page.getByLabel('Username').fill(username)
  await page.getByLabel('Password').fill(password)
  await page.getByRole('button', { name: 'Sign in' }).click()

  // After login the user lands on `/` and stays there: `/` is the whole app,
  // and activating a workspace no longer changes the URL. (Earlier versions had
  // to tolerate a redirect to `/o/{username}` and then to `/workspace/{id}`;
  // both are gone, so this can match exactly.)
  //
  // If a transient error occurs (e.g. hub DB not yet ready after restart), retry.
  // Each attempt waits 10s, for a total of 30s matching the original timeout.
  const loggedInURL = /\/$/
  for (let attempt = 0; attempt < 3; attempt++) {
    try {
      await expect(page).toHaveURL(loggedInURL)
      await page.waitForLoadState('networkidle')
      return // success
    }
    catch {
      // Check if there's an error message on the login page
      const error = page.locator('[class*="error"], [class*="Error"]')
      if (await error.count() > 0) {
        // Transient error — retry sign-in
        await page.getByRole('button', { name: 'Sign in' }).click()
        continue
      }
      // No error visible — page may still be loading. On the last attempt, give up.
      if (attempt === 2) {
        throw new Error('loginViaUI timed out')
      }
      // Otherwise, keep waiting (next iteration will check URL again)
    }
  }
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
  // Wait for the active tab's context first, for the same reason
  // openTerminalViaUI does: handleOpenAgent reads `{workerId, workingDir}`
  // SYNCHRONOUSLY on click and, finding either missing, opens the "new agent"
  // dialog to ask the user instead. That is a one-shot bail with no retry, so a
  // click that lands too early creates no tab at all and the count wait below
  // burns its whole budget against a number that will never change -- which is
  // exactly how 036 failed.
  await waitForActiveTabContext(page)
  // Count existing agent tabs so we can wait for the new one to appear.
  const tabsBefore = await page.locator('[data-testid="tab"][data-tab-type="agent"]').count()
  await page.locator('[data-testid^="new-agent-button"]').first().click()
  // Wait for the new agent tab to appear (the API call is async)
  await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(tabsBefore + 1)
  // Wait for the new tab to become selected and its editor to be ready
  await expect(page.locator('[data-testid="tab"][data-tab-type="agent"][aria-selected="true"]')).toBeVisible()
  await expect(page.locator('[data-testid="chat-editor"] .ProseMirror')).toBeVisible()
}

/**
 * Wait until the app has resolved a WORKING DIRECTORY for the active tab.
 *
 * `waitForWorkspaceReady` returns as soon as a tab element exists, which happens
 * the moment the CRDT projection lands -- but a projected tab carries only
 * tile/position/worker. Everything else, `workingDir` included, arrives later
 * from the worker (`useTabHydrators` -> `ListAgents` / `ListTerminals`). Any
 * action whose precondition is the active tab's directory therefore races page
 * load, and loses on a cold instance.
 *
 * Waits on the Files section's RESOLVED working dir, which the app publishes as
 * `data-working-dir` -- the app's own statement that the context has landed, not
 * a proxy we invented for the tests.
 *
 * Emphatically NOT the tree root node, which this used to wait for. That node is
 * gated only on `workerId` (available from the CRDT TabRecord immediately) and
 * renders against a `props.workingDir || '~'` fallback, so it attaches while the
 * dir is still empty -- i.e. it was already on screen at exactly the moment the
 * race it was supposed to close was open, and the specs kept their original
 * flake. A non-empty `data-working-dir` cannot be produced by the fallback.
 *
 * `attached`, NOT `visible`: whether the sidebar is EXPANDED is a layout question
 * (it collapses under the mobile-layout breakpoint, so a spec that shrinks the
 * viewport first would wait forever on a visibility check) and is orthogonal to
 * whether the directory resolved. Attachment is exactly the half we mean.
 *
 * Fails loudly on timeout rather than proceeding, so a genuinely broken
 * hydration surfaces here instead of as a confusing downstream assertion.
 */
export async function waitForActiveTabContext(page: Page) {
  await page.locator('[data-working-dir]:not([data-working-dir=""])').first().waitFor({ state: 'attached' })
}

/**
 * Open a new terminal in the currently selected workspace.
 * Clicks the terminal button in the tab bar.
 *
 * Waits for the active tab's context first: the handler reads the directory
 * SYNCHRONOUSLY on click and, finding none, opens the "new terminal" dialog to
 * ask the user instead -- a one-shot bail with no retry, so a click that lands
 * too early yields no terminal at all and every later assertion times out.
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
  await page.getByRole('button', { name: 'Sign up' }).click()
}

/**
 * Logout via the app menu in the custom titlebar.
 */
export async function logoutViaUI(page: Page) {
  await page.getByTestId('app-menu-trigger').first().click()
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
 * The app does not store preferences as bare JSON: every `leapmux:`-family
 * value goes through `~/lib/browserStorage`, which wraps it as `{ v, e }` with
 * an expiration and sweeps any entry whose wrapper is missing or malformed.
 *
 * These two helpers therefore have to speak the wrapper, and both silently did
 * the wrong thing when they did not. Reading `JSON.parse(raw)[field]` yields
 * the WRAPPER's fields, so `getBrowserPref` returned null for every preference
 * that was actually set; writing a bare object made `setInitialBrowserPref` a
 * no-op, because `loadBrowserPrefs` rejects an unwrapped entry and falls back
 * to `{}`. Neither failed loudly -- the specs just asserted against defaults.
 *
 * The key and its TTL come from the app's own registry rather than being
 * restated here, so a change on that side cannot leave the tests writing an
 * entry the sweep would drop.
 */
function browserPrefsTtlMs(): number {
  const ttlMs = EXACT_KEY_TTLS.get(KEY_BROWSER_PREFS)
  if (ttlMs === undefined)
    throw new Error(`${KEY_BROWSER_PREFS} is missing from EXACT_KEY_TTLS`)
  return ttlMs
}

const BROWSER_PREFS_TTL_MS = browserPrefsTtlMs()

/** Read a single field from the consolidated browser preferences in localStorage. */
export async function getBrowserPref(page: Page, field: string): Promise<string | null> {
  return page.evaluate(([key, f]) => {
    const raw = localStorage.getItem(key)
    if (!raw)
      return null
    const wrapper = JSON.parse(raw)
    const prefs = wrapper?.v
    if (prefs == null || typeof prefs !== 'object')
      return null
    return prefs[f] !== undefined ? String(prefs[f]) : null
  }, [KEY_BROWSER_PREFS, field] as const)
}

/** Set a single field in the consolidated browser preferences via addInitScript. */
export async function setInitialBrowserPref(page: Page, field: string, value: string) {
  await page.addInitScript(([key, f, v, ttlMs]) => {
    let prefs: Record<string, unknown> = {}
    const raw = localStorage.getItem(key)
    if (raw) {
      try {
        const wrapper = JSON.parse(raw)
        if (wrapper?.v != null && typeof wrapper.v === 'object')
          prefs = wrapper.v as Record<string, unknown>
      }
      catch {
        // Malformed: start from empty, exactly as the app's reader does.
      }
    }
    prefs[f] = v
    localStorage.setItem(key, JSON.stringify({ v: prefs, e: Date.now() + ttlMs }))
  }, [KEY_BROWSER_PREFS, field, value, BROWSER_PREFS_TTL_MS] as const)
}

/**
 * Set the session cookie in the browser context so subsequent navigations
 * are authenticated. The token is a cookie string like "leapmux-session=<value>".
 * Must be called **before** any page.goto() calls.
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
 * The status bar's chip TRIGGERS — the labels the user actually sees.
 *
 * Assertions must go through these, never through the bar itself. Each chip's
 * popover is a DOM sibling that stays mounted while closed, so the bar's own
 * `textContent` also contains every option label of every axis: asserting
 * `toContainText('Plan Mode')` on the bar passes whatever mode is selected,
 * because "Plan Mode" is one of the options in the closed mode list.
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
 * Close every open popover, so a menu interaction starts from a known state.
 *
 * The composer's menus nest: the `[+]` popover holds one submenu popover per
 * option group. A submenu left open from a previous step covers the `[+]`
 * menu's other items, and a click on one of them is then intercepted — which
 * shows up as a retry loop that runs to the test timeout rather than as a
 * readable failure.
 *
 * Driven through `hidePopover()` rather than Escape because Escape only reaches
 * a popover that holds focus, and the caller cannot know which one does.
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
 * Click `trigger` unless it already reports an open popover, then confirm it is
 * open.
 *
 * Waits on `aria-expanded`, which is the app's own statement of whether its
 * popover is open, rather than on the popover's visibility. A menu caught
 * mid-CLOSE is still visible for the length of its animation, so a visibility
 * check saw "already open", skipped the click, and handed the caller a menu
 * that vanished a frame later -- the caller's click on an item then failed with
 * "element is not stable" and finally "element is not visible", which is
 * exactly how 044's model switches died under load.
 *
 * Call inside a `toPass` block: it is idempotent, so a retry re-establishes an
 * open that a re-render closed.
 */
async function ensureExpanded(trigger: Locator) {
  if (await trigger.getAttribute('aria-expanded') !== 'true')
    await trigger.click()
  await expect(trigger).toHaveAttribute('aria-expanded', 'true')
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
 * The `[+]` menu's submenu trigger for one option group.
 *
 * Present only while the agent OFFERS that group, so its absence is how the new
 * composer says "this axis does not apply" — the fused menu it replaced listed
 * every group at once and expressed the same thing by omitting the group's
 * items. Requires the `[+]` menu to be open.
 */
export function settingsGroupTrigger(page: Page, groupId: string): Locator {
  return page.locator(`[data-testid="composer-group-${groupId}"]`)
}

/**
 * Open one option group's settings submenu and leave it open.
 *
 * Drives the composer's `[+]` menu rather than the status-bar chips: the `[+]`
 * menu carries EVERY group (the status bar shows only model/effort/mode), and
 * it stays rendered at any width, while the chips are hidden below the `sm`
 * breakpoint and can be switched off entirely by the "Show status bar"
 * preference.
 *
 * Opens BOTH triggers inside one `toPass` block rather than calling
 * `openPlusMenu` first: a settings round-trip that lands between the two closes
 * the `[+]` menu, and only a retry that re-opens both recovers from it.
 */
export async function openSettingsMenu(page: Page, groupId: string): Promise<Locator> {
  const plus = page.locator('[data-testid="composer-plus-trigger"]')
  const submenu = settingsGroupTrigger(page, groupId)
  await expect(plus).toBeVisible()
  // Start closed: a submenu left open from a previous group covers this one's
  // trigger, and the click below would be intercepted.
  await closeComposerMenus(page)
  await expect(async () => {
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
 * A DirectoryTree row, located by the file or directory name it displays.
 *
 * Anchored on the row's own test id rather than on `getByText(name)`: the label
 * is duplicated into the Tooltip portal for every truncated node, so a bare
 * text locator matches twice and dies on Playwright's strict-mode check.
 *
 * `:visible`-scoped before `.first()` for the same reason as {@link workspaceRow}:
 * the sidebar is mounted twice, and `.first()` alone can pick the off-screen
 * copy's row -- hoverable in the accessibility sense, but covered, so the hover
 * is refused until it times out.
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
 * Wait until the chat editor's draft for `text` has actually reached
 * localStorage.
 *
 * The draft is written on a debounce, so a reload issued before that timer
 * fires drops it. Sleeping "the debounce plus a margin" is what made the draft
 * specs flaky: the margin is sized against a real timer, and under a loaded
 * machine the debounce itself lands late, so the sleep expires first and the
 * reload races the write. Poll the state the reload depends on instead.
 */
export async function waitForEditorDraft(page: Page, text: string) {
  await expect.poll(async () => page.evaluate(
    ([prefix, needle]) => {
      for (let i = 0; i < localStorage.length; i++) {
        const key = localStorage.key(i)
        // The stored value is the `{ v, e }` wrapper browserStorage writes, so
        // the draft text sits inside its JSON.
        if (key?.startsWith(prefix) && (localStorage.getItem(key) ?? '').includes(needle))
          return true
      }
      return false
    },
    [PREFIX_EDITOR_DRAFT, text] as const,
  ), `the editor draft "${text}" must be persisted before the reload`).toBe(true)
}

/**
 * Rename a tab through its double-click inline editor and wait for the label
 * to settle.
 *
 * A rename is the only thing that writes a terminal's persisted title: the
 * worker gives every terminal the name `Terminal <Name>` at creation, and a PTY-driven
 * OSC title is a live overlay that is deliberately never persisted (see the
 * SignalTitle case in the worker's terminal.go).
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
 * Open a sidebar row's hover-revealed menu and leave it open, with `item` on
 * screen.
 *
 * Hover, trigger click, AND the item lookup are retried as ONE unit, because
 * the menu is transient in two different ways. The tree re-renders on
 * git-status refreshes and on every turn-end trigger, detaching the row under
 * the pointer ("element is not stable", then "element was detached from the
 * DOM"); and the sidebar itself re-renders on workspace / worker / todo
 * changes. Waiting on a generic "the menu opened" signal and THEN reaching for
 * the item left exactly that gap: 014 and 037 both failed with the menu open
 * and the item they wanted gone.
 *
 * Shared by the file-tree three-dot menu and the branch-group one, which differ
 * only in how their trigger is addressed.
 */
async function openRowMenu(row: Locator, trigger: Locator, item: Locator) {
  await expect(async () => {
    if (!await item.isVisible()) {
      await row.hover()
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
async function clickRowMenuItem(row: Locator, trigger: Locator, item: Locator) {
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
 * The first branch-group row in the sidebar.
 *
 * `:visible`-scoped for the same reason as {@link workspaceRow}: the app mounts
 * the sidebar twice, and an unscoped `.first()` can land on the OFF-SCREEN
 * copy's row -- which is visible enough to hover but sits under the on-screen
 * sidebar, so the hover is refused with "subtree intercepts pointer events"
 * until the action times out.
 *
 * Its menu trigger is addressed by `aria-expanded` rather than by position:
 * DropdownMenu renders its items as `<button role="menuitem">` inside the row's
 * own popover, so `.locator('button').last()` resolves to a hidden menu ITEM
 * and never becomes clickable. Only the trigger carries `aria-expanded`.
 */
export function branchGroupRow(page: Page): Locator {
  return page.locator(`[data-testid="tab-tree-branch-group"]${VISIBLE}`).first()
}

function branchMenuTrigger(row: Locator): Locator {
  return row.locator('[aria-expanded]').first()
}

/** Open a branch group's three-dot menu, with `requiredItem` on screen. */
export async function openBranchMenu(page: Page, row: Locator, requiredItem = 'Change branch...') {
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
 * Wait until the agent has reported its option catalog.
 *
 * Waits on the `[+]` menu's model submenu, not the status-bar chip: the chip
 * lives inside a surface the "Show status bar" preference removes, so a spec
 * that switches the bar off would block here until the global timeout.
 *
 * The submenu exists only for a group that exists and offers at least one
 * option, so its PRESENCE is the app's own marker that the catalog landed --
 * nothing invented for the tests. A freshly opened tab shows no model group at
 * all for as long as its agent takes to hand over its groups, so any assertion
 * about a model's name has to wait for this.
 */
export async function waitForSettingsHydrated(page: Page) {
  const plus = page.locator('[data-testid="composer-plus-trigger"]')
  await expect(plus).toBeVisible()
  await expect(async () => {
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
    if (await page.locator('[data-testid="empty-tile-actions"]').first().isVisible().catch(() => false))
      return true
    if (await page.locator('[data-testid="empty-tile-hint"]').first().isVisible().catch(() => false))
      return true
    return false
  }, pollOpts).toBe(true)
}

/**
 * The sidebar row for `workspaceId`, restricted to the one on screen.
 *
 * The app mounts the sidebar TWICE -- the desktop element and the
 * mobile/overlay one are separate `createLeftSidebarElement` calls -- so the
 * same `workspace-item-<id>` can exist in two subtrees with only one of them
 * rendered. Playwright reports the strict-mode violation on the resolved set
 * BEFORE applying its visibility filter, so even `waitFor()` (which waits for
 * `visible`) throws outright. 142 and 152 both died there.
 *
 * `.first()` on top of that, because during a layout-breakpoint transition BOTH
 * copies can be visible at once and `:visible` alone still resolves to two. It
 * is safe by construction: both nodes carry the same workspace id, so clicking
 * or reading either answers the same question.
 */
export function workspaceRow(page: Page, workspaceId: string): Locator {
  return page.locator(`[data-testid="workspace-item-${workspaceId}"]${VISIBLE}`).first()
}

/**
 * The tab-tree leaves nested under `workspaceId`'s sidebar row, read from the
 * ON-SCREEN sidebar.
 *
 * Built on {@link workspaceRow}, so it inherits the `:visible` scoping. The two
 * specs that needed this each hand-rolled it with a bare
 * `document.querySelector`, which takes the FIRST `workspace-item-<id>` in DOM
 * order -- and the app mounts the sidebar twice, so that is sometimes the
 * off-screen copy. Its leaves exist but never hydrate their worker-side
 * metadata, so a title assertion sat on the fallback "Agent" until it timed
 * out, reporting a hydration bug that was really a wrong-copy read.
 *
 * The children wrapper is the row's next sibling (the tree renders them as
 * siblings, not as descendants), hence the xpath hop.
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
 * Load the app and make `workspaceId` the active workspace, then wait for its
 * shell to be ready.
 *
 * There is no per-workspace URL to navigate to: `/` is the whole app, and which
 * workspace it opens on is decided by `resolveActiveWorkspace` from
 * localStorage. So this drives the same path a user does — load `/`, click the
 * sidebar row — rather than seeding storage, which would let the specs pass
 * against a broken restore.
 *
 * The click is skipped when the row is already active (a single-workspace
 * account, or a reload that restored the one we want), so this stays a no-op
 * rather than a redundant switch.
 *
 * NOTE the transit: when the target is NOT the workspace a cold start picks,
 * that other workspace is briefly activated first — which auto-expands its
 * sidebar row and hydrates its tabs. A spec asserting on sidebar expansion has
 * to establish its starting state explicitly rather than assume everything but
 * the target is collapsed (see 017's expanded-state-persists test).
 */
export async function openWorkspace(page: Page, workspaceId: string) {
  await page.goto('/')
  const row = workspaceRow(page, workspaceId)
  await row.waitFor()
  if (await row.getAttribute('data-active') !== 'true')
    await row.click()
  await expect(row).toHaveAttribute('data-active', 'true')
  await waitForWorkspaceReady(page)
}

/**
 * Reload the app at `/` and assert it came back on `workspaceId` without being
 * told to — i.e. that the persisted selection was restored.
 *
 * Deliberately does NOT click the sidebar row; that is the whole difference
 * from `openWorkspace`. A broken restore has to fail here rather than be
 * papered over by the click, which is what makes this the right helper for the
 * reload-and-come-back specs.
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
      clone.querySelectorAll('[data-testid="tab-close"], [data-testid="tab-notification"], [data-testid="tab-remote-badge"]').forEach(n => n.remove())
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
  // Capture the box INSIDE the poll and return that one. Checking for a box
  // and then reading it are two round trips, and the element can detach
  // between them — a workspace switch legitimately remounts the tile and its
  // tab strip, and these drags run right after one, so a re-read lands
  // mid-remount often enough to matter. `expect.poll` retries under the
  // global expect timeout.
  let box: { x: number, y: number, width: number, height: number } | null = null
  await expect.poll(async () => {
    box = await locator.boundingBox()
    return box !== null
  }).toBe(true)
  if (!box)
    throw new Error(`No bounding box for ${locator}`)
  return box
}
