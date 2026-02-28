import type { Page } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'

import { expect } from '@playwright/test'

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
  await expect(banner).toBeVisible({ timeout: 60_000 })
  return banner
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

/**
 * Login via the UI form. Navigates to /login, fills credentials,
 * and waits for redirect to the personal org page.
 */
export async function loginViaUI(page: Page, username = 'admin', password = 'admin') {
  await page.goto('/login')
  await page.getByLabel('Username').fill(username)
  await page.getByLabel('Password').fill(password)
  await page.getByRole('button', { name: 'Sign in' }).click()

  // After login, user is redirected to /o/${username}.
  // If a transient error occurs (e.g. hub DB not yet ready after restart), retry.
  // Each attempt waits 10s, for a total of 30s matching the original timeout.
  for (let attempt = 0; attempt < 3; attempt++) {
    try {
      await expect(page).toHaveURL(new RegExp(`/o/${username}`), { timeout: 10_000 })
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
 * Org selector defaults to the personal org.
 */
export async function approveWorkerViaUI(page: Page, token: string, name: string) {
  await page.goto(`/register/${token}`)
  await expect(page.getByRole('heading', { name: 'Approve Worker' })).toBeVisible()
  await page.getByPlaceholder('e.g. my-workstation').fill(name)
  await page.getByRole('button', { name: 'Approve' }).click()
  await expect(page.getByText('Worker Registered Successfully')).toBeVisible()
}

/**
 * Create a new workspace via the UI dialog.
 * The dialog fetches workers on mount. The Create button is disabled
 * until an online worker is selected. If the worker is temporarily
 * offline (e.g. bidi stream reconnecting), retries by clicking refresh.
 */
export async function createWorkspaceViaUI(page: Page, title: string, workingDir?: string) {
  // Click "+" button on a section header to open new workspace dialog
  await page.getByTitle(/New workspace/).first().click()
  await expect(page.getByRole('heading', { name: 'New Workspace' })).toBeVisible()

  // Scope to the dialog to avoid strict-mode violations with the sidebar
  // "Create a new workspace..." button.
  const dialog = page.getByRole('dialog')
  const createBtn = dialog.getByRole('button', { name: 'Create', exact: true })
  const refreshBtn = dialog.getByTitle('Refresh workers')

  // Wait for the initial fetch to find an online worker.
  // If not found, retry by clicking the refresh button (worker may be reconnecting).
  for (let attempt = 0; attempt < 6; attempt++) {
    try {
      await expect(createBtn).toBeEnabled()
      break
    }
    catch {
      if (attempt === 5)
        throw new Error('No online worker found after 6 attempts')
      await refreshBtn.click()
    }
  }

  // Fill in the form
  if (title) {
    await page.getByPlaceholder('New Workspace').fill(title)
  }

  // Set working directory via the DirectoryTree path input (scoped to dialog
  // to avoid ambiguity with the sidebar DirectoryTree)
  if (workingDir) {
    const pathInput = dialog.getByPlaceholder('Enter path...')
    await pathInput.fill(workingDir)
    await pathInput.press('Enter')
  }

  // Click Create
  await createBtn.click()

  // Wait for navigation to the new workspace page (uses unique workspace ID in URL).
  // This avoids strict-mode issues with duplicate workspace titles on retries.
  await expect(page).toHaveURL(/\/workspace\//)

  // Wait for the dialog to fully close. With many workspaces in the sidebar,
  // the UI re-render after workspace creation can delay dialog removal.
  // If the dialog is still visible after a short wait, press Escape to force-close.
  try {
    await expect(dialog).not.toBeVisible()
  }
  catch {
    // Dialog didn't close naturally — press Escape to dismiss it
    await page.keyboard.press('Escape')
    await expect(dialog).not.toBeVisible()
  }
}

/**
 * Open a new agent in the currently selected workspace.
 * Clicks the agent button in the tab bar which directly creates an agent.
 */
export async function openAgentViaUI(page: Page) {
  // Count existing agent tabs so we can wait for the new one to appear.
  const tabsBefore = await page.locator('[data-testid="tab"][data-tab-type="agent"]').count()
  await page.locator('[data-testid="new-agent-button"]').click()
  // Wait for the new agent tab to appear (the API call is async)
  await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(tabsBefore + 1)
  // Wait for the new tab to become selected and its editor to be ready
  await expect(page.locator('[data-testid="tab"][data-tab-type="agent"][aria-selected="true"]')).toBeVisible()
  await expect(page.locator('[data-testid="chat-editor"] .ProseMirror')).toBeVisible()
}

/**
 * Open a new terminal in the currently selected workspace.
 * Clicks the terminal button in the tab bar.
 */
export async function openTerminalViaUI(page: Page) {
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
  await page.getByLabel('Password', { exact: true }).fill(password)
  await page.getByLabel('Confirm Password').fill(password)
  await page.getByRole('button', { name: 'Sign up' }).click()
}

/**
 * Logout via the user menu at the bottom of the sidebar.
 */
export async function logoutViaUI(page: Page) {
  // Click the username trigger at the bottom of the sidebar
  await page.getByTestId('user-menu-trigger').first().click()
  // Click "Log out" in the popup menu
  await page.getByText('Log out').click()
  // Wait for redirect to login page
  await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible()
}

/**
 * Switch to a different organization via the user menu.
 */
export async function switchOrgViaUI(page: Page, orgName: string) {
  // Open user menu
  await page.getByTestId('user-menu-trigger').first().click()
  // Click the org name
  await page.getByText(orgName, { exact: true }).click()
  // Wait for navigation
  await expect(page).toHaveURL(new RegExp(`/o/${orgName}`))
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
export async function setInitialTheme(page: Page, theme: 'light' | 'dark' | 'system') {
  await page.addInitScript((t) => {
    localStorage.setItem('leapmux-theme', t)
  }, theme)
}

/**
 * Inject auth token into localStorage before navigation.
 * Uses addInitScript so the token is set before any page scripts run.
 * Must be called **before** any page.goto() calls.
 */
export async function loginViaToken(page: Page, token: string) {
  await page.addInitScript((t) => {
    localStorage.setItem('leapmux_token', t)
  }, token)
}

/**
 * Wait for the debounced layout save API call to complete.
 * Returns a promise that resolves once the SaveLayout response arrives.
 * Must be called **before** the action that triggers the save, then awaited
 * after the action completes.
 *
 * Usage:
 *   const saved = waitForLayoutSave(page)
 *   await doSomethingThatTriggersLayoutSave()
 *   await saved
 *   await page.reload()
 */
export function waitForLayoutSave(page: Page) {
  return page.waitForResponse(
    resp => resp.url().includes('WorkspaceService/SaveLayout') && resp.ok(),
    { timeout: 10_000 },
  )
}

/**
 * Wait for a workspace page to be fully loaded.
 * Waits for either a tab or the empty tile actions/hint.
 */
export async function waitForWorkspaceReady(page: Page) {
  await page.locator('[data-testid="tab"]')
    .or(page.locator('[data-testid="empty-tile-actions"]'))
    .or(page.locator('[data-testid="empty-tile-hint"]'))
    .first()
    .waitFor({ timeout: 15_000 })
}
