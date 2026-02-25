import process from 'node:process'
import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, loginViaToken, waitForWorkspaceReady } from './helpers'

test.describe('File Browser', () => {
  test('should display file entries when workspace is active', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    await loginViaToken(page, adminToken)
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'File Browser Test', adminOrgId, process.cwd())
    try {
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // The Files sidebar should be visible
      await expect(page.locator('[data-testid="section-header-files-summary"]')).toBeVisible()

      // Wait for file entries to load (working dir is the frontend dir)
      // package.json should exist in the frontend directory
      await expect(page.getByText('package.json')).toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should expand a directory in the tree', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    await loginViaToken(page, adminToken)
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'File Nav Test', adminOrgId, process.cwd())
    try {
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Wait for file entries to load
      await expect(page.getByText('src')).toBeVisible()

      // Click on the "src" directory to expand it in the tree
      await page.getByText('src').click()

      // Should show files inside src/ (app.tsx should be there after SolidStart migration)
      await expect(page.getByText('app.tsx')).toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })
})
