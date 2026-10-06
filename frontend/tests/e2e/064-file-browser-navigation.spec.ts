import { frontendRoot } from '~/test-support/sourceTree'
import { expect, test } from './fixtures'
import { sidebarSectionHeader, treeRow } from './helpers/ui'

test.describe('File Browser Navigation', () => {
  // The agent works in the frontend directory, so the tree shows its files.
  test.use({ agentWorkingDir: frontendRoot })

  test('should open file browser tab and show files', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace

    // The Files sidebar should be visible in the right panel
    await expect(sidebarSectionHeader(page, 'files')).toBeVisible()

    // Wait for file entries to load (working dir is the frontend dir)
    // package.json should exist in the frontend directory
    await expect(treeRow(page, 'package.json')).toBeVisible()
  })

  test('should navigate into a directory', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace

    // Wait for the tree to load — "src" directory should be visible.
    // `exact`, because `src-tauri` also contains the name.
    const src = treeRow(page, 'src', { exact: true })
    await expect(src).toBeVisible()

    // Click on "src" to expand/navigate into it
    await src.click()

    // Should show files inside src/ (app.tsx should be there)
    await expect(treeRow(page, 'app.tsx', { exact: true })).toBeVisible()
  })

  test('should navigate to parent directory', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace

    // Wait for the tree to load — "src" directory should be visible
    const src = treeRow(page, 'src', { exact: true })
    await expect(src).toBeVisible()

    // Navigate into "src"
    await src.click()
    const appFile = treeRow(page, 'app.tsx', { exact: true })
    await expect(appFile).toBeVisible()

    // Click on "src" again to collapse the directory (navigate back up)
    await src.click()

    // After collapsing, the child file "app.tsx" should no longer be visible
    await expect(appFile).not.toBeVisible()

    // The root-level entries should still be visible
    await expect(treeRow(page, 'package.json')).toBeVisible()
  })
})
