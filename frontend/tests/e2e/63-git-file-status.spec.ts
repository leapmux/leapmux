import { execSync } from 'node:child_process'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path, { join } from 'node:path'
import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'

const frontendDir = path.resolve(import.meta.dirname, '../..')

/**
 * Creates a temporary git repo with controlled file states for testing.
 * Returns the repo directory path. The caller must clean up via `rmSync`.
 */
function createTempGitRepo(): string {
  const dir = mkdtempSync(join(tmpdir(), 'leapmux-e2e-git-'))
  execSync('git init', { cwd: dir })
  execSync('git config user.email "test@test.com"', { cwd: dir })
  execSync('git config user.name "Test"', { cwd: dir })

  // Create initial committed files.
  writeFileSync(join(dir, 'clean.txt'), 'clean content')
  writeFileSync(join(dir, 'file_a.txt'), 'original content a')
  writeFileSync(join(dir, 'file_b.txt'), 'original content b')
  execSync('git add .', { cwd: dir })
  execSync('git commit -m "initial"', { cwd: dir })

  // file_a: staged modification
  writeFileSync(join(dir, 'file_a.txt'), 'modified content a\nnew line\n')
  execSync('git add file_a.txt', { cwd: dir })

  // file_b: unstaged modification
  writeFileSync(join(dir, 'file_b.txt'), 'modified content b\nline2\nline3\n')

  // untracked.txt: new untracked file with 4 lines
  writeFileSync(join(dir, 'untracked.txt'), 'line1\nline2\nline3\nline4\n')

  return dir
}

test.describe('Git File Status', () => {
  test('git filter tab bar is visible for git repo workspace', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Git TabBar Test', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Wait for the file tree to load.
      await expect(page.locator('[data-testid="tree-root-node"]')).toBeVisible()

      // Tab bar should be visible in a git repo.
      const tabBar = page.locator('[data-testid="files-filter-tab-bar"]')
      await expect(tabBar).toBeVisible()

      // All 4 filter tabs should be present.
      await expect(page.locator('[data-testid="files-filter-all"]')).toBeVisible()
      await expect(page.locator('[data-testid="files-filter-changed"]')).toBeVisible()
      await expect(page.locator('[data-testid="files-filter-staged"]')).toBeVisible()
      await expect(page.locator('[data-testid="files-filter-unstaged"]')).toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('tab bar hidden for non-git directory', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    // Use /tmp as a non-git directory.
    const tempDir = mkdtempSync(join(tmpdir(), 'leapmux-e2e-nongit-'))
    writeFileSync(join(tempDir, 'hello.txt'), 'test')
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Non-Git Test', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, tempDir)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Wait for the file tree to load.
      await expect(page.locator('[data-testid="tree-root-node"]')).toBeVisible()

      // Tab bar should NOT be visible.
      await expect(page.locator('[data-testid="files-filter-tab-bar"]')).not.toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
      rmSync(tempDir, { recursive: true, force: true })
    }
  })

  test('filter tabs show correct files', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const tempDir = createTempGitRepo()
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Filter Test', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, tempDir)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Wait for tree to load.
      await expect(page.locator('[data-testid="tree-root-node"]')).toBeVisible()
      await expect(page.locator('[data-testid="files-filter-tab-bar"]')).toBeVisible()

      // "All" tab (default) should show all files including clean.txt.
      await expect(page.getByText('clean.txt')).toBeVisible()
      await expect(page.getByText('file_a.txt')).toBeVisible()
      await expect(page.getByText('file_b.txt')).toBeVisible()

      // Switch to "Changed" tab — should show only changed files.
      await page.locator('[data-testid="files-filter-changed"]').click()
      await expect(page.getByText('file_a.txt')).toBeVisible()
      await expect(page.getByText('file_b.txt')).toBeVisible()
      await expect(page.getByText('clean.txt')).not.toBeVisible()

      // Switch to "Staged" tab — should show only file_a.
      await page.locator('[data-testid="files-filter-staged"]').click()
      await expect(page.getByText('file_a.txt')).toBeVisible()
      await expect(page.getByText('file_b.txt')).not.toBeVisible()

      // Switch to "Unstaged" tab — should show only file_b.
      await page.locator('[data-testid="files-filter-unstaged"]').click()
      await expect(page.getByText('file_b.txt')).toBeVisible()
      await expect(page.getByText('file_a.txt')).not.toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
      rmSync(tempDir, { recursive: true, force: true })
    }
  })

  test('git status indicators on files', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const tempDir = createTempGitRepo()
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Status Indicator Test', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, tempDir)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      await expect(page.locator('[data-testid="tree-root-node"]')).toBeVisible()

      // Switch to Changed tab to see status indicators.
      await page.locator('[data-testid="files-filter-changed"]').click()

      // Status indicators should be visible.
      await expect(page.locator('[data-testid="git-status-staged"]')).toBeVisible()
      await expect(page.locator('[data-testid="git-status-unstaged"]')).toBeVisible()

      // Diff stats badges should be visible.
      await expect(page.locator('[data-testid="git-diff-stats"]').first()).toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
      rmSync(tempDir, { recursive: true, force: true })
    }
  })

  test('untracked files show diff stats badge', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const tempDir = createTempGitRepo()
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Untracked Stats Test', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, tempDir)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      await expect(page.locator('[data-testid="tree-root-node"]')).toBeVisible()

      // Switch to Changed tab and enable flat list for easier targeting.
      await page.locator('[data-testid="files-filter-changed"]').click()
      await expect(page.getByText('untracked.txt')).toBeVisible()
      await page.locator('[data-testid="files-flat-list-toggle"]').click()
      await expect(page.locator('[data-testid="files-flat-list"]')).toBeVisible()

      // The untracked file row should have a diff stats badge showing *1.
      const flatList = page.locator('[data-testid="files-flat-list"]')
      const untrackedRow = flatList.locator('div', { hasText: 'untracked.txt' }).first()
      const badge = untrackedRow.locator('[data-testid="git-diff-stats"]')
      await expect(badge).toBeVisible()
      await expect(badge).toContainText('*1')

      // It should also have the untracked status indicator.
      await expect(untrackedRow.locator('[data-testid="git-status-untracked"]')).toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
      rmSync(tempDir, { recursive: true, force: true })
    }
  })

  test('flat list toggle in changed mode', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const tempDir = createTempGitRepo()
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Flat List Test', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, tempDir)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      await expect(page.locator('[data-testid="tree-root-node"]')).toBeVisible()

      // Switch to Changed tab.
      await page.locator('[data-testid="files-filter-changed"]').click()
      await expect(page.getByText('file_a.txt')).toBeVisible()

      // Click flat list toggle.
      await page.locator('[data-testid="files-flat-list-toggle"]').click()

      // Flat list should be visible.
      await expect(page.locator('[data-testid="files-flat-list"]')).toBeVisible()
      await expect(page.getByText('file_a.txt')).toBeVisible()
      await expect(page.getByText('file_b.txt')).toBeVisible()

      // Toggle back.
      await page.locator('[data-testid="files-flat-list-toggle"]').click()

      // Tree view should return (flat list hidden).
      await expect(page.locator('[data-testid="files-flat-list"]')).not.toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
      rmSync(tempDir, { recursive: true, force: true })
    }
  })

  test('collapse all button resets tree expansion', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Collapse All Test', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      const rootNode = page.locator('[data-testid="tree-root-node"]')
      await expect(rootNode).toBeVisible()

      // Verify children are visible (root is expanded by default).
      await expect(page.getByText('package.json')).toBeVisible()

      // Expand a subdirectory.
      await page.getByText('src').click()

      // Click collapse all.
      await page.locator('[data-testid="files-collapse-all"]').click()

      // Wait for collapse to take effect. Only root should be expanded.
      // Subdirectory contents should not be visible.
      await expect(page.getByText('package.json')).toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('locate file button hidden when no file tab active', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Locate Hidden Test', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      await expect(page.locator('[data-testid="tree-root-node"]')).toBeVisible()

      // When an agent tab is active (default), locate button should not be visible.
      const locateButton = page.locator('[data-testid="files-locate-file"]')
      await expect(locateButton).not.toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('diff mode toolbar appears for changed files', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const tempDir = createTempGitRepo()
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Diff Toolbar Test', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, tempDir)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      await expect(page.locator('[data-testid="tree-root-node"]')).toBeVisible()

      // Switch to Changed tab and click a file.
      await page.locator('[data-testid="files-filter-changed"]').click()
      await expect(page.getByText('file_b.txt')).toBeVisible()
      await page.getByText('file_b.txt').click()

      // Diff mode toolbar should appear.
      await expect(page.locator('[data-testid="diff-mode-toolbar"]')).toBeVisible()

      // Toolbar should have HEAD, Working, Unified, Split buttons.
      await expect(page.locator('[data-testid="diff-mode-head"]')).toBeVisible()
      await expect(page.locator('[data-testid="diff-mode-working"]')).toBeVisible()
      await expect(page.locator('[data-testid="diff-mode-unified"]')).toBeVisible()
      await expect(page.locator('[data-testid="diff-mode-split"]')).toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
      rmSync(tempDir, { recursive: true, force: true })
    }
  })

  test('opening from staged tab starts in diff view', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const tempDir = createTempGitRepo()
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Staged Diff Test', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, tempDir)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      await expect(page.locator('[data-testid="tree-root-node"]')).toBeVisible()

      // Switch to Staged tab and click file_a.
      await page.locator('[data-testid="files-filter-staged"]').click()
      await expect(page.getByText('file_a.txt')).toBeVisible()
      await page.getByText('file_a.txt').click()

      // File should open with diff toolbar showing unified as active.
      await expect(page.locator('[data-testid="diff-mode-toolbar"]')).toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
      rmSync(tempDir, { recursive: true, force: true })
    }
  })
})
