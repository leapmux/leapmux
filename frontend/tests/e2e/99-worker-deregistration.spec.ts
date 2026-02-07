import type { Buffer } from 'node:buffer'
import { spawn } from 'node:child_process'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { approveRegistrationViaAPI, deregisterWorkerViaAPI, loginViaUI } from './helpers'
import { expect, processTest as test } from './process-control-fixtures'

// This file spawns its own temporary worker so that deregistering it
// does not affect the main worker used by other tests in this file.
let tempWorkerPid: number | undefined
let tempWorkerId: string | undefined

function extractRegistrationToken(output: string): string | null {
  const jsonMatch = output.match(/"token"\s*:\s*"([^"]+)"/)
  const urlMatch = output.match(/\/register\/(\S+)/)
  return jsonMatch?.[1] ?? urlMatch?.[1] ?? null
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
      'deregister-test-worker',
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
    await loginViaUI(page)
    await page.goto('/o/admin/workers')
    await expect(page.getByRole('heading', { name: 'Workers' })).toBeVisible()

    // Find the deregister-test-worker card and open its context menu
    const card = page.locator('[data-testid="worker-card"]').filter({
      has: page.getByTestId('worker-name').filter({ hasText: 'deregister-test-worker' }),
    })
    await expect(card).toBeVisible()
    await card.getByTestId('worker-menu-trigger').click()
    await page.getByRole('menuitem', { name: 'Deregister' }).click()

    // Confirmation dialog should appear
    const dialog = page.getByTestId('worker-settings-dialog')
    await expect(dialog).toBeVisible()
    await expect(dialog.getByText('Worker Settings')).toBeVisible()

    // Warning about termination should be visible
    const warning = page.getByTestId('deregister-warning')
    await expect(warning).toBeVisible()
    await expect(warning).toContainText('terminate')

    // Cancel for now
    await page.getByTestId('deregister-cancel').click()
  })

  test('should cancel deregistration', async ({ page }) => {
    await loginViaUI(page)
    await page.goto('/o/admin/workers')
    await expect(page.getByRole('heading', { name: 'Workers' })).toBeVisible()

    // Find the deregister-test-worker card
    const card = page.locator('[data-testid="worker-card"]').filter({
      has: page.getByTestId('worker-name').filter({ hasText: 'deregister-test-worker' }),
    })
    await expect(card).toBeVisible()

    // Open deregister dialog
    await card.getByTestId('worker-menu-trigger').click()
    await page.getByRole('menuitem', { name: 'Deregister' }).click()
    await expect(page.getByTestId('worker-settings-dialog')).toBeVisible()

    // Cancel
    await page.getByTestId('deregister-cancel').click()
    await expect(page.getByTestId('worker-settings-dialog')).not.toBeVisible()

    // Worker should still be visible
    await expect(page.getByTestId('worker-name').filter({ hasText: 'deregister-test-worker' })).toBeVisible()
  })

  test('should deregister worker after confirmation', async ({ page }) => {
    await loginViaUI(page)
    await page.goto('/o/admin/workers')
    await expect(page.getByRole('heading', { name: 'Workers' })).toBeVisible()

    // Find the deregister-test-worker card
    const card = page.locator('[data-testid="worker-card"]').filter({
      has: page.getByTestId('worker-name').filter({ hasText: 'deregister-test-worker' }),
    })
    await expect(card).toBeVisible()

    // Open deregister dialog
    await card.getByTestId('worker-menu-trigger').click()
    await page.getByRole('menuitem', { name: 'Deregister' }).click()
    await expect(page.getByTestId('worker-settings-dialog')).toBeVisible()

    // Confirm deregistration
    await page.getByTestId('deregister-confirm').click()

    // Dialog should close
    await expect(page.getByTestId('worker-settings-dialog')).not.toBeVisible()

    // The deregister-test-worker should disappear from the list
    await expect(page.getByTestId('worker-name').filter({ hasText: 'deregister-test-worker' })).not.toBeVisible()

    // Mark as deregistered so afterAll doesn't try again
    tempWorkerId = undefined
  })

  test('should still show main worker after deregistration of temp worker', async ({ page }) => {
    await loginViaUI(page)
    await page.goto('/o/admin/workers')
    await expect(page.getByRole('heading', { name: 'Workers' })).toBeVisible()

    // The main test-worker should still be visible
    await expect(page.getByTestId('worker-name').filter({ hasText: 'test-worker' })).toBeVisible()
  })
})
