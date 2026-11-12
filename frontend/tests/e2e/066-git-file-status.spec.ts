import { execFileSync } from 'node:child_process'
import { rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { frontendRoot } from '~/test-support/sourceTree'
import { test } from './fixtures'
import { createTestDirectory } from './helpers/runDirectory'
import { treeRow } from './helpers/ui'
import { showWorkspaceWithAgents } from './helpers/workspace'
import { initGitRepo } from './helpers/worktree'

/**
 * Creates a temporary git repo with controlled file states for testing.
 * `initGitRepo` pins the settings that keep a second writer out of `.git`.
 * Returns the repo directory path. The caller must clean up via `rmSync`.
 */
function createTempGitRepo(): string {
  const dir = createTestDirectory('leapmux-e2e-git-')
  initGitRepo(dir)

  // Create initial committed files.
  writeFileSync(join(dir, 'clean.txt'), 'clean content')
  writeFileSync(join(dir, 'file_a.txt'), 'original content a')
  writeFileSync(join(dir, 'file_b.txt'), 'original content b')
  execFileSync('git', ['add', '.'], { cwd: dir })
  execFileSync('git', ['commit', '-m', 'initial'], { cwd: dir })

  // file_a: staged modification
  writeFileSync(join(dir, 'file_a.txt'), 'modified content a\nnew line\n')
  execFileSync('git', ['add', 'file_a.txt'], { cwd: dir })

  // file_b: unstaged modification
  writeFileSync(join(dir, 'file_b.txt'), 'modified content b\nline2\nline3\n')

  // untracked.txt: new untracked file with 4 lines
  writeFileSync(join(dir, 'untracked.txt'), 'line1\nline2\nline3\nline4\n')

  return dir
}

test.describe('Git File Status', () => {
  // Each case that uses the workspace fixture gets a fresh repository with the
  // controlled file states, and the repository goes after the test. A case that
  // reads another directory creates its workspace with `showWorkspaceWithAgents`.
  test.use({
    // eslint-disable-next-line no-empty-pattern -- Playwright requires a destructuring pattern for the fixture argument
    agentWorkingDir: async ({}, use) => {
      const dir = createTempGitRepo()
      try {
        await use(dir)
      }
      finally {
        rmSync(dir, { recursive: true, force: true })
      }
    },
  })

  test('git filter tab bar is visible for git repo workspace', async ({ page, leapmuxServer }) => {
    await showWorkspaceWithAgents(page, leapmuxServer, 'Git TabBar Test', { workingDir: frontendRoot })

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
  })

  test('tab bar hidden for non-git directory', async ({ page, leapmuxServer }) => {
    // Use a private directory with no Git repository.
    const tempDir = createTestDirectory('leapmux-e2e-nongit-')
    writeFileSync(join(tempDir, 'hello.txt'), 'test')
    try {
      await showWorkspaceWithAgents(page, leapmuxServer, 'Non-Git Test', { workingDir: tempDir })

      // Wait for the file tree to load.
      await expect(page.locator('[data-testid="tree-root-node"]')).toBeVisible()

      // Tab bar should NOT be visible.
      await expect(page.locator('[data-testid="files-filter-tab-bar"]')).not.toBeVisible()
    }
    finally {
      rmSync(tempDir, { recursive: true, force: true })
    }
  })

  test('filter tabs show correct files', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace

    // Wait for tree to load.
    await expect(page.locator('[data-testid="tree-root-node"]')).toBeVisible()
    await expect(page.locator('[data-testid="files-filter-tab-bar"]')).toBeVisible()

    // Find each file as a visible tree row, not as raw text. The label is
    // duplicated in the Tooltip portal, and the tooltip's text matches
    // getByText even when visually hidden, so a bare `getByText(...)`
    // assertion never goes to `not visible` after filtering.

    // "All" tab (default) should show all files including clean.txt.
    await expect(treeRow(page, 'clean.txt')).toBeVisible()
    await expect(treeRow(page, 'file_a.txt')).toBeVisible()
    await expect(treeRow(page, 'file_b.txt')).toBeVisible()

    // Select the filter and verify that it becomes active before checking the tree.
    // The unfiltered tree also satisfies the visible-file assertions.
    // A lost click must not make those assertions pass for the wrong reason.
    const selectFilter = async (key: string) => {
      const tab = page.locator(`[data-testid="files-filter-${key}"]`)
      await tab.click()
      // role=tab + aria-selected, not aria-pressed: picking a filter swaps the
      // region below it, which is a tab set rather than a row of toggles.
      await expect(tab).toHaveAttribute('aria-selected', 'true')
    }

    // Switch to "Changed" tab — should show only changed files.
    await selectFilter('changed')
    await expect(treeRow(page, 'file_a.txt')).toBeVisible()
    await expect(treeRow(page, 'file_b.txt')).toBeVisible()
    await expect(treeRow(page, 'clean.txt')).not.toBeVisible()

    // Switch to "Staged" tab — should show only file_a.
    await selectFilter('staged')
    await expect(treeRow(page, 'file_a.txt')).toBeVisible()
    await expect(treeRow(page, 'file_b.txt')).not.toBeVisible()

    // Switch to "Unstaged" tab — should show only file_b.
    await selectFilter('unstaged')
    await expect(treeRow(page, 'file_b.txt')).toBeVisible()
    await expect(treeRow(page, 'file_a.txt')).not.toBeVisible()
  })

  test('git status indicators on files', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace

    await expect(page.locator('[data-testid="tree-root-node"]')).toBeVisible()

    // Switch to Changed tab to see status indicators.
    await page.locator('[data-testid="files-filter-changed"]').click()

    // Status indicators should be visible.
    await expect(page.locator('[data-testid="git-status-staged"]')).toBeVisible()
    await expect(page.locator('[data-testid="git-status-unstaged"]')).toBeVisible()

    // Diff stats badges should be visible.
    await expect(page.locator('[data-testid="git-diff-stats"]').first()).toBeVisible()
  })

  test('untracked files show diff stats badge', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace

    await expect(page.locator('[data-testid="tree-root-node"]')).toBeVisible()

    // Switch to Changed tab and enable flat list for easier targeting.
    await page.locator('[data-testid="files-filter-changed"]').click()
    await expect(treeRow(page, 'untracked.txt')).toBeVisible()
    await page.locator('[data-testid="files-flat-list-toggle"]').click()
    await expect(page.locator('[data-testid="files-flat-list"]:visible')).toBeVisible()

    // The untracked file row should have a diff stats badge showing *1.
    const flatList = page.locator('[data-testid="files-flat-list"]:visible')
    const untrackedRow = flatList.locator('div', { hasText: 'untracked.txt' }).first()
    const badge = untrackedRow.locator('[data-testid="git-diff-stats"]')
    await expect(badge).toBeVisible()
    await expect(badge).toContainText('*1')

    // It should also have the untracked status indicator.
    await expect(untrackedRow.locator('[data-testid="git-status-untracked"]')).toBeVisible()
  })

  test('flat list toggle in changed mode', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace

    await expect(page.locator('[data-testid="tree-root-node"]')).toBeVisible()

    // Switch to Changed tab.
    await page.locator('[data-testid="files-filter-changed"]').click()
    await expect(treeRow(page, 'file_a.txt')).toBeVisible()

    // Click flat list toggle.
    await page.locator('[data-testid="files-flat-list-toggle"]').click()

    // Flat list should be visible. Its rows are not tree rows, so the names are
    // read inside the list.
    const flatList = page.locator('[data-testid="files-flat-list"]:visible')
    await expect(flatList).toBeVisible()
    await expect(flatList.getByText('file_a.txt')).toBeVisible()
    await expect(flatList.getByText('file_b.txt')).toBeVisible()

    // Toggle back.
    await page.locator('[data-testid="files-flat-list-toggle"]').click()

    // Tree view should return (flat list hidden).
    await expect(flatList).not.toBeVisible()
  })

  test('flat list honours the sort order', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    // The fixture's three changed files are 28, 31 and 24 bytes, so every sort
    // order below differs from the name order — an assertion here cannot pass
    // with the sort key ignored.
    await expect(page.locator('[data-testid="tree-root-node"]')).toBeVisible()

    await page.locator('[data-testid="files-filter-changed"]').click()
    await page.locator('[data-testid="files-flat-list-toggle"]').click()
    const rows = page.locator('[data-testid="files-flat-list"]:visible').locator(':scope > div')
    await expect(rows).toHaveCount(3)

    // Default: repo-relative path, ascending.
    await expect(rows.nth(0)).toContainText('file_a.txt')
    await expect(rows.nth(1)).toContainText('file_b.txt')
    await expect(rows.nth(2)).toContainText('untracked.txt')

    // Largest first — the sizes come from the worker's stat of each entry.
    await page.locator('[data-testid="files-sort-toggle"]:visible').click()
    await page.locator('[data-testid="files-sort-key-size"]:visible').click()
    await page.locator('[data-testid="files-sort-direction-desc"]:visible').click()
    await page.keyboard.press('Escape')

    await expect(rows.nth(0)).toContainText('file_b.txt')
    await expect(rows.nth(1)).toContainText('file_a.txt')
    await expect(rows.nth(2)).toContainText('untracked.txt')

    // Smallest first reverses the files.
    await page.locator('[data-testid="files-sort-toggle"]:visible').click()
    await page.locator('[data-testid="files-sort-direction-asc"]:visible').click()
    await page.keyboard.press('Escape')

    await expect(rows.nth(0)).toContainText('untracked.txt')
    await expect(rows.nth(1)).toContainText('file_a.txt')
    await expect(rows.nth(2)).toContainText('file_b.txt')
  })

  test('collapse all button resets tree expansion', async ({ page, leapmuxServer }) => {
    await showWorkspaceWithAgents(page, leapmuxServer, 'Collapse All Test', { workingDir: frontendRoot })

    const rootNode = page.locator('[data-testid="tree-root-node"]')
    await expect(rootNode).toBeVisible()

    // Verify children are visible (root is expanded by default).
    await expect(treeRow(page, 'package.json')).toBeVisible()

    // Expand a subdirectory.
    await treeRow(page, 'src', { exact: true }).click()

    // Click collapse all.
    await page.locator('[data-testid="files-collapse-all"]').click()

    // Wait for collapse to take effect. Only root should be expanded.
    // Subdirectory contents should not be visible.
    await expect(treeRow(page, 'package.json')).toBeVisible()
  })

  test('locate file button hidden when no file tab active', async ({ page, leapmuxServer }) => {
    await showWorkspaceWithAgents(page, leapmuxServer, 'Locate Hidden Test', { workingDir: frontendRoot })

    await expect(page.locator('[data-testid="tree-root-node"]')).toBeVisible()

    // When an agent tab is active (default), locate button should not be visible.
    const locateButton = page.locator('[data-testid="files-locate-file"]')
    await expect(locateButton).not.toBeVisible()
  })

  test('diff mode toolbar appears for changed files', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace

    await expect(page.locator('[data-testid="tree-root-node"]')).toBeVisible()

    // Switch to Changed tab and click a file.
    await page.locator('[data-testid="files-filter-changed"]').click()
    const changedFile = treeRow(page, 'file_b.txt')
    await expect(changedFile).toBeVisible()
    await changedFile.click()

    // Diff mode toolbar should appear.
    await expect(page.locator('[data-testid="diff-mode-toolbar"]')).toBeVisible()

    // Toolbar should have HEAD, Working, Unified, Split buttons.
    await expect(page.locator('[data-testid="diff-mode-head"]')).toBeVisible()
    await expect(page.locator('[data-testid="diff-mode-working"]')).toBeVisible()
    await expect(page.locator('[data-testid="diff-mode-unified"]')).toBeVisible()
    await expect(page.locator('[data-testid="diff-mode-split"]')).toBeVisible()
  })

  test('opening from staged tab starts in diff view', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace

    await expect(page.locator('[data-testid="tree-root-node"]')).toBeVisible()

    // Switch to Staged tab and click file_a.
    await page.locator('[data-testid="files-filter-staged"]').click()
    const stagedFile = treeRow(page, 'file_a.txt')
    await expect(stagedFile).toBeVisible()
    await stagedFile.click()

    // File should open with diff toolbar showing unified as active.
    await expect(page.locator('[data-testid="diff-mode-toolbar"]')).toBeVisible()
  })
})

test.describe('Git File Versions Past the Read Limit', () => {
  /**
   * The viewer reads at most 256 KiB of the working copy, and of the HEAD and staged versions too: the worker cuts a
   * git read at the limit and reports the whole size. The status bar says so for the version that the mode shows.
   */
  test('says that a long HEAD version is truncated, in diff and HEAD modes', async ({ page, leapmuxServer }) => {
    const dir = createTestDirectory('leapmux-e2e-git-long-')
    try {
      initGitRepo(dir)
      const lines = Array.from({ length: 8000 }, (_, index) => `line ${String(index).padStart(6, '0')} of a long tracked file`)
      writeFileSync(join(dir, 'long.txt'), `${lines.join('\n')}\n`)
      execFileSync('git', ['add', '.'], { cwd: dir })
      execFileSync('git', ['commit', '-m', 'initial'], { cwd: dir })
      writeFileSync(join(dir, 'long.txt'), `changed first line\n${lines.slice(1).join('\n')}\n`)

      await showWorkspaceWithAgents(page, leapmuxServer, 'Long Git File', { workingDir: dir })
      await page.locator('[data-testid="files-filter-changed"]').click()
      await treeRow(page, 'long.txt').click()
      await expect(page.locator('[data-testid="diff-mode-toolbar"]')).toBeVisible()

      const truncated = page.getByText(/Truncated at 256(\.0)? KB/i).filter({ visible: true })
      await expect(truncated).toBeVisible()

      await page.locator('[data-testid="diff-mode-head"]').click()
      await expect(page.getByText('line 000000 of a long tracked file').filter({ visible: true })).toBeVisible()
      await expect(truncated).toBeVisible()
    }
    finally {
      rmSync(dir, { recursive: true, force: true })
    }
  })
})

test.describe('Git Diff Context', () => {
  /**
   * The diff worker computes the diff with three unchanged lines around each change, and the gap separators hold
   * the rest of the file until a click reveals them.
   */
  test('draws a change in a long file with three lines of context, and reveals the rest on a click', async ({ page, leapmuxServer }) => {
    const dir = createTestDirectory('leapmux-e2e-git-context-')
    try {
      initGitRepo(dir)
      const rows = Array.from({ length: 250 }, (_, index) => `row ${String(index + 1).padStart(3, '0')}`)
      writeFileSync(join(dir, 'context.txt'), `${rows.join('\n')}\n`)
      execFileSync('git', ['add', '.'], { cwd: dir })
      execFileSync('git', ['commit', '-m', 'initial'], { cwd: dir })
      // Insert one line after row 100.
      writeFileSync(join(dir, 'context.txt'), `${[
        ...rows.slice(0, 100),
        'inserted line',
        ...rows.slice(100),
      ].join('\n')}\n`)

      await showWorkspaceWithAgents(page, leapmuxServer, 'Diff Context', { workingDir: dir })
      await page.locator('[data-testid="files-filter-changed"]').click()
      await treeRow(page, 'context.txt').click()
      await expect(page.locator('[data-testid="diff-mode-toolbar"]')).toBeVisible()

      const visibleText = (text: string) => page.getByText(text, { exact: true }).filter({ visible: true })
      await expect(visibleText('inserted line')).toBeVisible()
      for (const row of [
        'row 098',
        'row 099',
        'row 100',
        'row 101',
        'row 102',
        'row 103',
      ])
        await expect(visibleText(row)).toBeVisible()
      await expect(visibleText('row 050')).toHaveCount(0)
      await expect(visibleText('row 200')).toHaveCount(0)

      // Rows 1 to 97 before the change, and rows 104 to 250 after it.
      await expect(visibleText('147 lines hidden')).toBeVisible()
      await visibleText('97 lines hidden').click()

      await expect(visibleText('row 050')).toBeVisible()
      await expect(visibleText('row 001')).toBeVisible()
      await expect(visibleText('row 200')).toHaveCount(0)
    }
    finally {
      rmSync(dir, { recursive: true, force: true })
    }
  })
})
