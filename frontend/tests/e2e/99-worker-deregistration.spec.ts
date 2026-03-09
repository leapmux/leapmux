import type { Buffer } from 'node:buffer'
import { spawn } from 'node:child_process'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { approveRegistrationViaAPI, deregisterWorkerViaAPI } from './helpers/api'
import { loginViaUI } from './helpers/ui'
import { expect, restartWorker, stopWorker, processTest as test, waitForWorkerOffline } from './process-control-fixtures'

// This file spawns its own temporary worker so that deregistering it
// does not affect the main worker used by other tests in this file.
let tempWorkerPid: number | undefined
let tempWorkerId: string | undefined

function extractRegistrationToken(output: string): string | null {
  const jsonMatch = output.match(/"token"\s*:\s*"([^"]+)"/)
  const urlMatch = output.match(/\/register\/(\S+)/)
  return jsonMatch?.[1] ?? urlMatch?.[1] ?? null
}

/**
 * Navigate to a workspace and expand the Workers sidebar section.
 * Returns the workers section locator.
 */
async function openWorkersSidebar(page: import('@playwright/test').Page) {
  await loginViaUI(page)
  const workersSection = page.getByTestId('section-header-workers')
  await expect(workersSection).toBeVisible()
  // Expand the section if collapsed by checking the DOM open property
  const isOpen = await workersSection.evaluate((el: HTMLDetailsElement) => el.open)
  if (!isOpen)
    await workersSection.locator('> summary').click()
  // Wait for content to be visible
  await expect(workersSection.locator('[class*="sectionItems"]')).toBeVisible()
  return workersSection
}

/**
 * Find a worker item by name within the Workers section and open its context menu.
 */
async function openWorkerContextMenu(
  page: import('@playwright/test').Page,
  workersSection: import('@playwright/test').Locator,
  workerName: string,
) {
  const workerItem = workersSection.getByText(workerName).locator('..')
  await expect(workerItem).toBeVisible()
  await workerItem.hover()
  await workerItem.locator('button[aria-expanded]').click()
  return workerItem
}

test.describe('Worker Deregistration', () => {
  test.beforeAll(async ({ separateHubWorker }) => {
    const { hubUrl, adminToken, adminOrgId, dataDir, binaryPath } = separateHubWorker
    const workerDataDir = join(dataDir, 'worker-deregister-data')
    mkdirSync(workerDataDir, { recursive: true })

    // Spawn a temporary worker
    const workerProc = spawn(binaryPath, [
      'worker',
      '-hub',
      hubUrl,
      '-data-dir',
      workerDataDir,
    ], {
      stdio: ['ignore', 'pipe', 'pipe'],
      env: { ...process.env, LEAPMUX_WORKER_NAME: 'deregister-test-worker' },
    })
    tempWorkerPid = workerProc.pid

    // Wait for registration token
    const registrationToken = await new Promise<string>((resolve, reject) => {
      let output = ''
      const timeout = setTimeout(() => {
        reject(new Error('Timed out waiting for temp worker registration token'))
      }, 30_000)

      const onData = (chunk: Buffer) => {
        const text = chunk.toString()
        output += text
        const token = extractRegistrationToken(output)
        if (token) {
          clearTimeout(timeout)
          workerProc.stderr?.off('data', onData)
          workerProc.stdout?.off('data', onData)
          workerProc.stderr?.resume()
          workerProc.stdout?.resume()
          resolve(token)
        }
      }

      workerProc.stderr?.on('data', onData)
      workerProc.stdout?.on('data', onData)
    })

    // Approve the temporary worker via API
    tempWorkerId = await approveRegistrationViaAPI(
      hubUrl,
      adminToken,
      registrationToken,
      adminOrgId,
    )
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
    await page.getByRole('menuitem', { name: 'Deregister' }).click()

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
    await page.getByRole('menuitem', { name: 'Deregister' }).click()
    await expect(page.getByTestId('worker-settings-dialog')).toBeVisible()

    // Cancel
    await page.getByTestId('deregister-cancel').click()
    await expect(page.getByTestId('worker-settings-dialog')).not.toBeVisible()

    // Worker should still be visible
    await expect(workersSection.getByText('deregister-test-worker')).toBeVisible()
  })

  test('should deregister worker after confirmation', async ({ page }) => {
    const workersSection = await openWorkersSidebar(page)

    // Open deregister dialog
    await openWorkerContextMenu(page, workersSection, 'deregister-test-worker')
    await page.getByRole('menuitem', { name: 'Deregister' }).click()
    await expect(page.getByTestId('worker-settings-dialog')).toBeVisible()

    // Confirm deregistration
    await page.getByTestId('deregister-confirm').click()

    // Dialog should close
    await expect(page.getByTestId('worker-settings-dialog')).not.toBeVisible()

    // The deregister-test-worker should disappear from the list
    await expect(workersSection.getByText('deregister-test-worker')).not.toBeVisible()

    // Mark as deregistered so afterAll doesn't try again
    tempWorkerId = undefined
  })

  test('should still show main worker after deregistration of temp worker', async ({ page }) => {
    const workersSection = await openWorkersSidebar(page)

    // The main test-worker should still be visible
    await expect(workersSection.getByText('test-worker', { exact: true })).toBeVisible()
  })
})

test.describe('Worker Status Indicator', () => {
  test('should show red status dot when worker goes offline and green when back online', async ({ page, separateHubWorker }) => {
    const workersSection = await openWorkersSidebar(page)

    // Worker should initially be connected (green)
    await expect(workersSection.locator('[data-status="connected"]')).toBeVisible()

    // Stop the worker
    await stopWorker()
    await waitForWorkerOffline(separateHubWorker.hubUrl, separateHubWorker.adminToken)

    // Status dot should change to disconnected (red)
    await expect(workersSection.locator('[data-status="disconnected"]')).toBeVisible({ timeout: 15_000 })

    // Restart the worker
    await restartWorker(separateHubWorker)

    // Reload the page so the frontend re-fetches workers and re-establishes
    // E2EE channels (channel status reflects E2EE state, not backend online/offline).
    await page.reload()
    const refreshedSection = page.getByTestId('section-header-workers')
    await expect(refreshedSection).toBeVisible()
    const reopened = await refreshedSection.evaluate((el: HTMLDetailsElement) => el.open)
    if (!reopened)
      await refreshedSection.locator('> summary').click()

    // Status dot should change back to connected (green)
    await expect(refreshedSection.locator('[data-status="connected"]')).toBeVisible({ timeout: 15_000 })
  })
})
