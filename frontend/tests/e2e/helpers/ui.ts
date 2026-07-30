import type { Locator, Page } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'

import { expect } from '@playwright/test'

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
// Common UI interaction helpers
// ──────────────────────────────────────────────

/** Send a message via the ProseMirror editor. */
export async function sendMessage(page: Page, text: string) {
  const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
  await expect(editor).toBeVisible()
  await editor.click()
  await page.keyboard.type(text, { delay: 100 })
  await page.keyboard.press('Meta+Enter')
}

/** Wait for the control request banner to appear and return a scoped locator. */
export async function waitForControlBanner(page: Page) {
  const banner = page.locator('[data-testid="control-banner"]')
  await expect(banner).toBeVisible()
  return banner
}

/** CSS selector for agent message bubbles. Exported for use in browser-context code (e.g. waitForFunction). */
export const ASSISTANT_BUBBLE_SELECTOR = '[data-testid="message-bubble"][data-role="agent"]'

/** Return a locator for all assistant message bubbles. */
export function assistantBubbles(page: Page) {
  return page.locator(ASSISTANT_BUBBLE_SELECTOR)
}

/** Return a locator for the first assistant message bubble. */
export function firstAssistantBubble(page: Page) {
  return page.locator(ASSISTANT_BUBBLE_SELECTOR).first()
}

/** Return a locator for the last assistant message bubble. */
export function lastAssistantBubble(page: Page) {
  return page.locator(ASSISTANT_BUBBLE_SELECTOR).last()
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

/** Wait for the agent to finish its current turn (thinking indicator gone). */
export async function waitForAgentIdle(page: Page, timeoutMs = 120_000) {
  // Brief delay so the thinking indicator has time to appear before we
  // wait for it to disappear.
  await page.waitForTimeout(2000)
  await expect(page.locator('[data-testid="thinking-indicator"]'))
    .not
    .toBeVisible({ timeout: timeoutMs })
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
 * Set the initial UI theme in localStorage before navigation.
 * Must be called before any page.goto() calls.
 */
const BROWSER_PREFS_KEY = 'leapmux:browser-prefs'

/** Read a single field from the consolidated browser preferences in localStorage. */
export async function getBrowserPref(page: Page, field: string): Promise<string | null> {
  return page.evaluate(([key, f]) => {
    const raw = localStorage.getItem(key)
    if (!raw)
      return null
    const prefs = JSON.parse(raw)
    return prefs[f] !== undefined ? String(prefs[f]) : null
  }, [BROWSER_PREFS_KEY, field] as const)
}

/** Set a single field in the consolidated browser preferences via addInitScript. */
export async function setInitialBrowserPref(page: Page, field: string, value: string) {
  await page.addInitScript(([key, f, v]) => {
    const raw = localStorage.getItem(key)
    const prefs = raw ? JSON.parse(raw) : {}
    prefs[f] = v
    localStorage.setItem(key, JSON.stringify(prefs))
  }, [BROWSER_PREFS_KEY, field, value] as const)
}

export async function setInitialTheme(page: Page, theme: 'light' | 'dark' | 'system') {
  await setInitialBrowserPref(page, 'theme', theme)
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

/** Open the settings menu, retrying if it was caught mid-close animation. */
export async function openSettingsMenu(page: Page) {
  const trigger = page.locator('[data-testid="agent-settings-trigger"]')
  const menu = page.locator('[data-testid="agent-settings-menu"]')
  await expect(trigger).toBeVisible()
  await expect(async () => {
    if (!await menu.isVisible()) {
      await trigger.click()
    }
    await expect(menu).toBeVisible()
  }).toPass()
}

/** Wait for the settings loading spinner to disappear. */
export async function waitForSettingsIdle(page: Page) {
  await expect(page.locator('[data-testid="settings-loading-spinner"]')).not.toBeVisible()
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
  const row = page.locator(`[data-testid="workspace-item-${workspaceId}"]`)
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
  await expect(page.locator(`[data-testid="workspace-item-${workspaceId}"]`))
    .toHaveAttribute('data-active', 'true')
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
