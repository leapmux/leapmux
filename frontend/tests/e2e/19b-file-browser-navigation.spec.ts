import process from 'node:process'
import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, loginViaToken, waitForWorkspaceReady } from './helpers'

test.describe('File Browser Navigation', () => {
  test('should open file browser tab and show files', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'File Browser Nav Test', adminOrgId, process.cwd())
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // The Files sidebar should be visible in the right panel
      await expect(page.locator('[data-testid="section-header-files-summary"]')).toBeVisible()

      // Wait for file entries to load (working dir is the frontend dir)
      // package.json should exist in the frontend directory
      await expect(page.getByText('package.json')).toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should navigate into a directory', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'File Nav Into Dir', adminOrgId, process.cwd())
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Wait for the tree to load — "src" directory should be visible
      await expect(page.getByText('src')).toBeVisible()

      // Click on "src" to expand/navigate into it
      await page.getByText('src').click()

      // Should show files inside src/ (app.tsx should be there)
      await expect(page.getByText('app.tsx')).toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should navigate to parent directory', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'File Nav Parent Dir', adminOrgId, process.cwd())
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Wait for the tree to load — "src" directory should be visible
      await expect(page.getByText('src')).toBeVisible()

      // Navigate into "src"
      await page.getByText('src').click()
      await expect(page.getByText('app.tsx')).toBeVisible()

      // Click on "src" again to collapse the directory (navigate back up)
      await page.getByText('src').click()

      // After collapsing, the child file "app.tsx" should no longer be visible
      await expect(page.getByText('app.tsx')).not.toBeVisible()

      // The root-level entries should still be visible
      await expect(page.getByText('package.json')).toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })
})
