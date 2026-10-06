import type { Locator, Page } from '@playwright/test'
import type { RegisteredWorker } from './helpers/nativeWorker'
import { join } from 'node:path'
import { escapeRegExp } from '../../src/lib/regexp'
import { deregisterWorkerViaAPI, listWorkersViaAPI } from './helpers/api'
import { finishCleanup } from './helpers/cleanup'
import { spawnRegisteredWorker } from './helpers/nativeWorker'
import { stopProcess } from './helpers/process'
import { expandSidebarSection, expectAnyVisible, openAppAs, sidebarSectionHeader } from './helpers/ui'
import { expect, restartWorker, SEPARATE_WORKER_NAME, stopWorker, processTest as test, waitForWorkerOffline } from './process-control-fixtures'

/** The name of the temporary Worker that this file deregisters. */
const TEMP_WORKER_NAME = 'deregister-test-worker'

// This file registers its own temporary Worker, so that the deregistration
// leaves the main Worker of the separate Hub to the other tests.
let tempWorker: RegisteredWorker | undefined

/**
 * Open the app as the administrator of the separate Hub and expand the Workers sidebar section.
 * Returns the workers section locator.
 */
async function openWorkersSidebar(page: Page, adminToken: string): Promise<Locator> {
  await openAppAs(page, adminToken)
  const workersSection = sidebarSectionHeader(page, 'workers')
  await expandSidebarSection(workersSection)
  // The workers list has its own container: it fills the section width so a
  // row clips its name, unlike the workspace list, which sizes to its widest row.
  await expect(workersSection.getByTestId('worker-list')).toBeVisible()
  return workersSection
}

/**
 * Find a worker item by name within the Workers section and open its context menu.
 * Uses the `worker-row` testid scoped to the matching worker-name span so the
 * lookup doesn't collide with name text inside an open WorkerContextMenu popover.
 */
async function openWorkerContextMenu(page: Page, workersSection: Locator, workerName: string) {
  const workerItem = workersSection
    .getByTestId('worker-row')
    .filter({ has: page.getByTestId('worker-name').filter({ hasText: workerName }) })
  await expect(workerItem).toBeVisible()
  await workerItem.hover()
  await workerItem.locator('button[aria-expanded]').click()
  return workerItem
}

test.describe('Worker Deregistration', () => {
  // These tests are ORDER-DEPENDENT: "should deregister worker after
  // confirmation" removes the temporary Worker, and "should still show main
  // worker after deregistration" reads the world that left behind. They rely
  // on playwright.config.ts keeping fullyParallel off; if that ever flips, this
  // describe needs `test.describe.configure({ mode: 'serial' })`.

  test.beforeAll(async ({ separateHubWorker }) => {
    tempWorker = await spawnRegisteredWorker(separateHubWorker, {
      name: TEMP_WORKER_NAME,
      dataDir: join(separateHubWorker.dataDir, 'worker-deregister-data'),
      // The lines of the temporary Worker join the server log that a failed test attaches.
      output: separateHubWorker.output,
    })
  })

  test.afterAll(async ({ separateHubWorker }) => {
    const worker = tempWorker
    tempWorker = undefined
    if (!worker)
      return
    const { hubUrl, adminToken } = separateHubWorker
    // Both steps run, and a failure of either one fails the hook.
    await finishCleanup([
      stopProcess(worker.proc),
      (async () => {
        // The hub stops listing the Worker once a test deregisters it. A Worker that the hub still lists goes away here.
        const workers = await listWorkersViaAPI(hubUrl, adminToken)
        if (workers.some(listed => listed.id === worker.workerId))
          await deregisterWorkerViaAPI(hubUrl, adminToken, worker.workerId)
      })(),
    ])
  })

  test('should show confirmation dialog with worker details', async ({ page, separateHubWorker }) => {
    const workersSection = await openWorkersSidebar(page, separateHubWorker.adminToken)

    // Open context menu for the temp worker and click Deregister
    await openWorkerContextMenu(page, workersSection, TEMP_WORKER_NAME)
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

  test('should cancel deregistration', async ({ page, separateHubWorker }) => {
    const workersSection = await openWorkersSidebar(page, separateHubWorker.adminToken)

    // Open deregister dialog
    await openWorkerContextMenu(page, workersSection, TEMP_WORKER_NAME)
    await page.getByRole('menuitem', { name: 'Deregister...' }).click()
    await expect(page.getByTestId('worker-settings-dialog')).toBeVisible()

    // Cancel
    await page.getByTestId('deregister-cancel').click()
    await expect(page.getByTestId('worker-settings-dialog')).not.toBeVisible()

    // Worker should still be visible
    await expect(workersSection.getByTestId('worker-name').filter({ hasText: TEMP_WORKER_NAME })).toBeVisible()
  })

  test('should deregister worker after confirmation', async ({ page, separateHubWorker }) => {
    const workersSection = await openWorkersSidebar(page, separateHubWorker.adminToken)

    // Open deregister dialog
    await openWorkerContextMenu(page, workersSection, TEMP_WORKER_NAME)
    await page.getByRole('menuitem', { name: 'Deregister...' }).click()
    await expect(page.getByTestId('worker-settings-dialog')).toBeVisible()

    // Confirm deregistration
    await page.getByTestId('deregister-confirm').click()

    // Dialog should close
    await expect(page.getByTestId('worker-settings-dialog')).not.toBeVisible()

    // The deregister-test-worker should disappear from the list
    await expect(workersSection.getByTestId('worker-name').filter({ hasText: TEMP_WORKER_NAME })).not.toBeVisible()
  })

  test('should still show main worker after deregistration of temp worker', async ({ page, separateHubWorker }) => {
    const workersSection = await openWorkersSidebar(page, separateHubWorker.adminToken)

    // The deregister-test-worker should be gone
    await expect(workersSection.getByTestId('worker-name').filter({ hasText: TEMP_WORKER_NAME })).not.toBeVisible()

    // The main worker should still be listed.
    // Worker names are fetched via E2EE and may not be available on the
    // app home (no active workspace), so check for the worker name OR
    // the em-dash fallback that appears when the name is unavailable.
    await expectAnyVisible(
      workersSection.getByTestId('worker-name').filter({ hasText: new RegExp(`^${escapeRegExp(SEPARATE_WORKER_NAME)}$`) }),
      workersSection.getByTestId('worker-name').filter({ hasText: /^—$/ }),
    )
  })
})

test.describe('Worker Status Indicator', () => {
  test('should show red status dot when worker goes offline and green when back online', async ({ page, authenticatedWorkspace, separateHubWorker }) => {
    // Navigate to a workspace so E2EE channels are established
    // (channel status requires E2EE, which isn't available on the app home alone).
    const workersSection = sidebarSectionHeader(page, 'workers')
    await expandSidebarSection(workersSection)

    // Worker should initially be connected (green)
    await expect(workersSection.locator('[data-status="connected"]')).toBeVisible()

    // Stop the worker
    await stopWorker(separateHubWorker)
    await waitForWorkerOffline(separateHubWorker)

    // Status dot should change to disconnected (red)
    await expect(workersSection.locator('[data-status="disconnected"]')).toBeVisible()

    // Restart the worker
    await restartWorker(separateHubWorker)

    // Reload the page so the frontend re-fetches workers and re-establishes
    // E2EE channels (channel status reflects E2EE state, not backend online/offline).
    // The section locator finds the header of the reloaded page.
    await page.reload()
    await expandSidebarSection(workersSection)

    // Status dot should change back to connected (green)
    await expect(workersSection.locator('[data-status="connected"]')).toBeVisible()
  })
})
