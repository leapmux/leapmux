import type { Buffer } from 'node:buffer'
import { spawn } from 'node:child_process'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import {
  deregisterWorkerViaAPI,
  listOnlineWorkerIDsViaAPI,
  mintRegistrationKeyViaAPI,
  waitForNewOnlineWorkerViaAPI,
} from './helpers/api'
import { expectAnyVisible, loginViaUI } from './helpers/ui'
import { expect, restartWorker, stopWorker, processTest as test, waitForWorkerOffline } from './process-control-fixtures'

// This file spawns its own temporary worker so that deregistering it
// does not affect the main worker used by other tests in this file.
let tempWorkerPid: number | undefined
let tempWorkerId: string | undefined

/**
 * Navigate to a workspace and expand the Workers sidebar section.
 * Returns the workers section locator.
 */
async function openWorkersSidebar(page: import('@playwright/test').Page) {
  await loginViaUI(page)
  const workersSection = page.getByTestId('section-header-workers')
  await expect(workersSection).toBeVisible()
  // Expand the section if collapsed by checking the DOM open property
  const isOpen = await workersSection.evaluate(el => !el.hasAttribute('data-closed'))
  if (!isOpen)
    await workersSection.locator('> [role="button"]').click()
  // Wait for content to be visible
  await expect(workersSection.locator('[class*="sectionItems"]')).toBeVisible()
  return workersSection
}

/**
 * Find a worker item by name within the Workers section and open its context menu.
 * Uses the `worker-row` testid scoped to the matching worker-name span so the
 * lookup doesn't collide with name text inside an open WorkerContextMenu popover.
 */
async function openWorkerContextMenu(
  page: import('@playwright/test').Page,
  workersSection: import('@playwright/test').Locator,
  workerName: string,
) {
  const workerItem = workersSection
    .getByTestId('worker-row')
    .filter({ has: page.getByTestId('worker-name').filter({ hasText: workerName }) })
  await expect(workerItem).toBeVisible()
  await workerItem.hover()
  await workerItem.locator('button[aria-expanded]').click()
  return workerItem
}

test.describe('Worker Deregistration', () => {
  test.beforeAll(async ({ separateHubWorker }) => {
    const { hubUrl, adminToken, dataDir, binaryPath } = separateHubWorker
    const workerDataDir = join(dataDir, 'worker-deregister-data')
    mkdirSync(workerDataDir, { recursive: true })

    // Mint a registration key (new flow from #216).
    const registrationKey = await mintRegistrationKeyViaAPI(hubUrl, adminToken)
    const beforeIds = new Set(await listOnlineWorkerIDsViaAPI(hubUrl, adminToken))

    // Spawn a temporary worker
    const workerProc = spawn(binaryPath, [
      'worker',
      '--hub',
      hubUrl,
      '--registration-key',
      registrationKey,
      '--data-dir',
      workerDataDir,
    ], {
      stdio: ['ignore', 'pipe', 'pipe'],
      env: { ...process.env, LEAPMUX_WORKER_NAME: 'deregister-test-worker' },
    })
    tempWorkerPid = workerProc.pid
    workerProc.stderr?.on('data', (c: Buffer) => process.stderr.write(`[TEMP-WORKER-ERR] ${c}`))
    workerProc.stdout?.on('data', (c: Buffer) => process.stderr.write(`[TEMP-WORKER-OUT] ${c}`))

    tempWorkerId = await waitForNewOnlineWorkerViaAPI(hubUrl, adminToken, beforeIds)
  })

  test.afterAll(async ({ separateHubWorker }) => {
    // Clean up: kill the temporary worker process
    if (tempWorkerPid) {
      try {
        process.kill(tempWorkerPid, 'SIGTERM')
      }
      catch {
        // Process may already be dead
      }
    }
    // Deregister via API if UI test didn't complete
    if (tempWorkerId) {
      try {
        await deregisterWorkerViaAPI(separateHubWorker.hubUrl, separateHubWorker.adminToken, tempWorkerId)
      }
      catch {
        // May already be deregistered
      }
    }
  })

  test('should show confirmation dialog with worker details', async ({ page }) => {
    const workersSection = await openWorkersSidebar(page)

    // Open context menu for the temp worker and click Deregister
    await openWorkerContextMenu(page, workersSection, 'deregister-test-worker')
    await page.getByRole('menuitem', { name: 'Deregister...' }).click()

    // Confirmation dialog should appear
    const dialog = page.getByTestId('worker-settings-dialog')
    await expect(dialog).toBeVisible()
    await expect(dialog.getByText('Deregister Worker')).toBeVisible()

    // Warning about termination should be visible
    const warning = page.getByTestId('deregister-warning')
    await expect(warning).toBeVisible()
    await expect(warning).toContainText('terminate')

    // Cancel for now
    await page.getByTestId('deregister-cancel').click()
  })

  test('should cancel deregistration', async ({ page }) => {
    const workersSection = await openWorkersSidebar(page)

    // Open deregister dialog
    await openWorkerContextMenu(page, workersSection, 'deregister-test-worker')
    await page.getByRole('menuitem', { name: 'Deregister...' }).click()
    await expect(page.getByTestId('worker-settings-dialog')).toBeVisible()

    // Cancel
    await page.getByTestId('deregister-cancel').click()
    await expect(page.getByTestId('worker-settings-dialog')).not.toBeVisible()

    // Worker should still be visible
    await expect(workersSection.getByTestId('worker-name').filter({ hasText: 'deregister-test-worker' })).toBeVisible()
  })

  test('should deregister worker after confirmation', async ({ page }) => {
    const workersSection = await openWorkersSidebar(page)

    // Open deregister dialog
    await openWorkerContextMenu(page, workersSection, 'deregister-test-worker')
    await page.getByRole('menuitem', { name: 'Deregister...' }).click()
    await expect(page.getByTestId('worker-settings-dialog')).toBeVisible()

    // Confirm deregistration
    await page.getByTestId('deregister-confirm').click()

    // Dialog should close
    await expect(page.getByTestId('worker-settings-dialog')).not.toBeVisible()

    // The deregister-test-worker should disappear from the list
    await expect(workersSection.getByTestId('worker-name').filter({ hasText: 'deregister-test-worker' })).not.toBeVisible()

    // Mark as deregistered so afterAll doesn't try again
    tempWorkerId = undefined
  })

  test('should still show main worker after deregistration of temp worker', async ({ page }) => {
    const workersSection = await openWorkersSidebar(page)

    // The deregister-test-worker should be gone
    await expect(workersSection.getByTestId('worker-name').filter({ hasText: 'deregister-test-worker' })).not.toBeVisible()

    // The main worker should still be listed.
    // Worker names are fetched via E2EE and may not be available on the
    // org page (no active workspace), so check for the worker name OR
    // the em-dash fallback that appears when the name is unavailable.
    await expectAnyVisible(
      workersSection.getByTestId('worker-name').filter({ hasText: /^test-worker$/ }),
      workersSection.getByTestId('worker-name').filter({ hasText: /^\u2014$/ }),
    )
  })
})

test.describe('Worker Status Indicator', () => {
  test('should show red status dot when worker goes offline and green when back online', async ({ page, authenticatedWorkspace, separateHubWorker }) => {
    // Navigate to a workspace so E2EE channels are established
    // (channel status requires E2EE, which isn't available on the org page alone).
    const workersSection = page.getByTestId('section-header-workers')
    await expect(workersSection).toBeVisible()
    const isOpen = await workersSection.evaluate(el => !el.hasAttribute('data-closed'))
    if (!isOpen)
      await workersSection.locator('> [role="button"]').click()

    // Worker should initially be connected (green)
    await expect(workersSection.locator('[data-status="connected"]')).toBeVisible()

    // Stop the worker
    await stopWorker()
    await waitForWorkerOffline(separateHubWorker.hubUrl, separateHubWorker.adminToken)

    // Status dot should change to disconnected (red)
    await expect(workersSection.locator('[data-status="disconnected"]')).toBeVisible()

    // Restart the worker
    await restartWorker(separateHubWorker)

    // Reload the page so the frontend re-fetches workers and re-establishes
    // E2EE channels (channel status reflects E2EE state, not backend online/offline).
    await page.reload()
    const refreshedSection = page.getByTestId('section-header-workers')
    await expect(refreshedSection).toBeVisible()
    const reopened = await refreshedSection.evaluate(el => !el.hasAttribute('data-closed'))
    if (!reopened)
      await refreshedSection.locator('> [role="button"]').click()

    // Status dot should change back to connected (green)
    await expect(refreshedSection.locator('[data-status="connected"]')).toBeVisible()
  })
})
