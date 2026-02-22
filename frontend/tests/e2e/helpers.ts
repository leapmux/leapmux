import type { Page } from '@playwright/test'
import type { AddressInfo } from 'node:net'
import type { E2EGlobalState } from './global-setup'
import { mkdirSync, readFileSync } from 'node:fs'
import { createServer } from 'node:net'
import { join } from 'node:path'
import process from 'node:process'

import { expect } from '@playwright/test'

// ──────────────────────────────────────────────
// Global state (read from file written by global-setup)
// ──────────────────────────────────────────────

let cachedGlobalState: E2EGlobalState | null = null

export function getGlobalState(): E2EGlobalState {
  if (cachedGlobalState)
    return cachedGlobalState

  const statePath = process.env.E2E_STATE_PATH
  if (!statePath)
    throw new Error('E2E_STATE_PATH env var is not set')

  cachedGlobalState = JSON.parse(readFileSync(statePath, 'utf-8'))
  return cachedGlobalState!
}

// ──────────────────────────────────────────────
// Server utilities
// ──────────────────────────────────────────────

export function findFreePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const server = createServer()
    server.listen(0, () => {
      const { port } = server.address() as AddressInfo
      server.close(() => resolve(port))
    })
    server.on('error', reject)
  })
}

export function waitForServer(url: string, timeoutMs = 30_000): Promise<void> {
  const start = Date.now()
  return new Promise((resolve, reject) => {
    const check = () => {
      fetch(url).then(() => resolve()).catch(() => {
        if (Date.now() - start > timeoutMs) {
          reject(new Error(`Server at ${url} did not start within ${timeoutMs}ms`))
        }
        else {
          setTimeout(check, 500)
        }
      })
    }
    check()
  })
}

// ──────────────────────────────────────────────
// Toast recording for e2e debugging
// ──────────────────────────────────────────────

export interface RecordedToast {
  message: string
  variant: string
  timestamp: number
}

/**
 * Install a toast recorder on the page.
 * This monkey-patches `window.ot.toast` so that every toast message is
 * captured in `window.__recordedToasts` for later retrieval.
 *
 * Must be called **before** navigating to the app (e.g. before loginViaUI).
 * Works across page reloads because it uses `addInitScript`.
 */
export async function installToastRecorder(page: Page) {
  await page.addInitScript(() => {
    ;(window as any).__recordedToasts = [] as RecordedToast[]

    // Intercept window.ot assignment to monkey-patch toast() and toastEl()
    let _ot: any
    Object.defineProperty(window, 'ot', {
      configurable: true,
      get() {
        return _ot
      },
      set(val: any) {
        if (val && typeof val.toast === 'function') {
          const original = val.toast
          const patched = function (message: string, title?: string, options?: any) {
            ;(window as any).__recordedToasts.push({
              message,
              variant: options?.variant ?? '',
              timestamp: Date.now(),
            })
            return original.call(val, message, title, options)
          }
          // Preserve .clear method
          patched.clear = original.clear
          val.toast = patched
        }
        if (val && typeof val.toastEl === 'function') {
          const originalEl = val.toastEl
          val.toastEl = function (element: HTMLElement, options?: any) {
            const msg = element.querySelector('.toast-message')
            ;(window as any).__recordedToasts.push({
              message: msg?.textContent ?? '',
              variant: element.getAttribute('data-variant') ?? '',
              timestamp: Date.now(),
            })
            return originalEl.call(val, element, options)
          }
        }
        _ot = val
      },
    })
  })
}

/**
 * Retrieve all toast messages recorded since the last page load or clear.
 */
export async function getRecordedToasts(page: Page): Promise<RecordedToast[]> {
  return page.evaluate(() => (window as any).__recordedToasts ?? [])
}

/**
 * Clear the recorded toast list.
 */
export async function clearRecordedToasts(page: Page) {
  await page.evaluate(() => {
    ;(window as any).__recordedToasts = []
  })
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

  // Wait for the initial fetch to find an online worker.
  // If not found, retry by clicking the refresh button (worker may be reconnecting).
  const createBtn = page.getByRole('button', { name: 'Create' })
  const refreshBtn = page.getByTitle('Refresh workers')
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
    const dialog = page.getByRole('dialog')
    const pathInput = dialog.getByPlaceholder('Enter path...')
    await pathInput.fill(workingDir)
    await pathInput.press('Enter')
  }

  // Click Create
  await page.getByRole('button', { name: 'Create' }).click()

  // Wait for navigation to the new workspace page (uses unique workspace ID in URL).
  // This avoids strict-mode issues with duplicate workspace titles on retries.
  await expect(page).toHaveURL(/\/workspace\//)

  // Wait for the dialog to fully close. With many workspaces in the sidebar,
  // the UI re-render after workspace creation can delay dialog removal.
  // If the dialog is still visible after a short wait, press Escape to force-close.
  const dialog = page.locator('[role="dialog"]')
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

// ──────────────────────────────────────────────
// API helpers for setting up test prerequisites
// ──────────────────────────────────────────────

/**
 * Login via the Connect API. Returns the auth token.
 */
export async function loginViaAPI(hubUrl: string, username: string, password: string): Promise<string> {
  const res = await fetch(`${hubUrl}/leapmux.v1.AuthService/Login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password }),
  })
  if (!res.ok) {
    throw new Error(`loginViaAPI failed: ${res.status}`)
  }
  const data = await res.json() as { token: string }
  return data.token
}

/**
 * Sign up a new user via the Connect API. Returns the auth token.
 */
export async function signUpViaAPI(
  hubUrl: string,
  username: string,
  password: string,
  displayName = '',
  email = '',
): Promise<string> {
  const res = await fetch(`${hubUrl}/leapmux.v1.AuthService/SignUp`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password, displayName, email }),
  })
  if (!res.ok) {
    throw new Error(`signUpViaAPI failed: ${res.status}`)
  }
  const data = await res.json() as { token: string }
  return data.token
}

/**
 * Enable signup via admin API.
 */
export async function enableSignupViaAPI(hubUrl: string, adminToken: string): Promise<void> {
  const res = await fetch(`${hubUrl}/leapmux.v1.AdminService/UpdateSettings`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${adminToken}`,
    },
    body: JSON.stringify({ settings: { signupEnabled: true } }),
  })
  if (!res.ok) {
    throw new Error(`enableSignupViaAPI failed: ${res.status}`)
  }
}

/**
 * Get the admin user's personal org ID via the Connect API.
 */
export async function getAdminOrgId(hubUrl: string, token: string): Promise<string> {
  const res = await fetch(`${hubUrl}/leapmux.v1.OrgService/ListMyOrgs`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`,
    },
    body: JSON.stringify({}),
  })
  if (!res.ok) {
    throw new Error(`getAdminOrgId failed: ${res.status}`)
  }
  const data = await res.json() as { orgs: Array<{ id: string, name: string, isPersonal?: boolean }> }
  const org = data.orgs.find(o => o.isPersonal) ?? data.orgs[0]
  if (!org) {
    throw new Error('No org found for admin user')
  }
  return org.id
}

/**
 * Get the first worker ID from the ListWorkers API.
 */
export async function getWorkerId(hubUrl: string, token: string): Promise<string> {
  const res = await fetch(`${hubUrl}/leapmux.v1.WorkerManagementService/ListWorkers`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`,
    },
    body: JSON.stringify({}),
  })
  if (!res.ok) {
    throw new Error(`getWorkerId failed: ${res.status}`)
  }
  const data = await res.json() as { workers: Array<{ id: string }> }
  if (!data.workers?.length) {
    throw new Error('No workers found')
  }
  return data.workers[0].id
}

/**
 * Invite a user to an org via the Connect API.
 */
export async function inviteToOrgViaAPI(
  hubUrl: string,
  token: string,
  orgId: string,
  username: string,
): Promise<void> {
  const res = await fetch(`${hubUrl}/leapmux.v1.OrgService/InviteOrgMember`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`,
    },
    body: JSON.stringify({ orgId, username, role: 'ORG_MEMBER_ROLE_MEMBER' }),
  })
  if (!res.ok) {
    throw new Error(`inviteToOrgViaAPI failed: ${res.status}`)
  }
}

/**
 * Update worker sharing via the Connect API.
 */
export async function shareWorkerViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
  shareMode: 'SHARE_MODE_PRIVATE' | 'SHARE_MODE_ORG' | 'SHARE_MODE_MEMBERS',
): Promise<void> {
  const res = await fetch(`${hubUrl}/leapmux.v1.WorkerManagementService/UpdateWorkerSharing`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`,
    },
    body: JSON.stringify({ workerId, shareMode }),
  })
  if (!res.ok) {
    throw new Error(`shareWorkerViaAPI failed: ${res.status}`)
  }
}

/**
 * Create a workspace via the Connect API. Returns the workspace ID.
 */
export async function createWorkspaceViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
  title: string,
  orgId: string,
  workingDir?: string,
): Promise<string> {
  const res = await fetch(`${hubUrl}/leapmux.v1.WorkspaceService/CreateWorkspace`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`,
    },
    body: JSON.stringify({ workerId, title, orgId, workingDir }),
  })
  if (!res.ok) {
    const body = await res.text().catch(() => '')
    throw new Error(`createWorkspaceViaAPI failed: ${res.status} ${body}`)
  }
  const data = await res.json() as { workspace: { id: string } }
  return data.workspace.id
}

/**
 * Update workspace sharing via the Connect API.
 */
export async function shareWorkspaceViaAPI(
  hubUrl: string,
  token: string,
  workspaceId: string,
  shareMode: 'SHARE_MODE_PRIVATE' | 'SHARE_MODE_ORG' | 'SHARE_MODE_MEMBERS',
): Promise<void> {
  const res = await fetch(`${hubUrl}/leapmux.v1.WorkspaceService/UpdateWorkspaceSharing`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`,
    },
    body: JSON.stringify({ workspaceId, shareMode }),
  })
  if (!res.ok) {
    throw new Error(`shareWorkspaceViaAPI failed: ${res.status}`)
  }
}

/**
 * Deregister a worker via the Connect API.
 */
export async function deregisterWorkerViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
): Promise<void> {
  const res = await fetch(`${hubUrl}/leapmux.v1.WorkerManagementService/DeregisterWorker`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`,
    },
    body: JSON.stringify({ workerId }),
  })
  if (!res.ok) {
    throw new Error(`deregisterWorkerViaAPI failed: ${res.status}`)
  }
}

/**
 * Approve a worker registration via the Connect API. Returns the worker ID.
 */
export async function approveRegistrationViaAPI(
  hubUrl: string,
  token: string,
  registrationToken: string,
  name: string,
  orgId: string,
): Promise<string> {
  const res = await fetch(`${hubUrl}/leapmux.v1.WorkerManagementService/ApproveRegistration`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`,
    },
    body: JSON.stringify({ registrationToken, name, orgId }),
  })
  if (!res.ok) {
    throw new Error(`approveRegistrationViaAPI failed: ${res.status}`)
  }
  const data = await res.json() as { workerId: string }
  return data.workerId
}

/**
 * Delete (soft-delete) a workspace via the Connect API.
 */
export async function deleteWorkspaceViaAPI(
  hubUrl: string,
  token: string,
  workspaceId: string,
): Promise<void> {
  const res = await fetch(`${hubUrl}/leapmux.v1.WorkspaceService/DeleteWorkspace`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`,
    },
    body: JSON.stringify({ workspaceId }),
  })
  if (!res.ok) {
    throw new Error(`deleteWorkspaceViaAPI failed: ${res.status}`)
  }
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
 * Wait for a workspace page to be fully loaded.
 * Waits for either a tab or the "No tabs in this tile" empty state.
 */
export async function waitForWorkspaceReady(page: Page) {
  await page.locator('[data-testid="tab"]')
    .or(page.getByText('No tabs in this tile'))
    .first()
    .waitFor({ timeout: 15_000 })
}
