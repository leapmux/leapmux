import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { frontendRoot } from '~/test-support/sourceTree'
import { test } from './fixtures'
import { createTestDirectory } from './helpers/runDirectory'
import { sidebarSectionHeader, treeRow } from './helpers/ui'
import { showWorkspaceWithAgents } from './helpers/workspace'

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

test.describe('File Viewer ANSI Output', () => {
  /**
   * A `.log` file is ANSI output. The main thread colours it with the synchronous Shiki highlighter, which holds no
   * grammar: Shiki colours ANSI without one. This drives that highlighter in a real browser, with the theme pair that the
   * app registers on it at boot.
   */
  test('colours the ANSI escapes of a log file', async ({ page, leapmuxServer }) => {
    const dir = createTestDirectory('leapmux-ansi-log-')
    writeFileSync(join(dir, 'build.log'), 'plain start\n\x1B[31mERROR failed\x1B[0m\nplain end\n')
    await showWorkspaceWithAgents(page, leapmuxServer, 'ANSI Log', { workingDir: dir })

    await treeRow(page, 'build.log').click()

    const error = page.locator('span[data-shiki-token]:visible', { hasText: 'ERROR' })
    const plain = page.locator('span[data-shiki-token]:visible', { hasText: 'plain start' })
    await expect(error).toHaveCount(1)
    await expect(plain).toHaveCount(1)
    // The escapes are gone from the text, and the red run has a colour of its own.
    await expect(error).toHaveText('ERROR failed')
    const colourOf = (token: typeof error) => token.evaluate(element => getComputedStyle(element).color)
    expect(await colourOf(error)).not.toBe(await colourOf(plain))
  })
})
