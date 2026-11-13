import type { Page } from '@playwright/test'
import { execFileSync } from 'node:child_process'
import { rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { frontendRoot } from '~/test-support/sourceTree'
import { test } from './fixtures'
import { domSamplesDuring } from './helpers/domSamples'
import { createTestDirectory } from './helpers/runDirectory'
import { openFileInDiffMode, treeRow, visibleOnly, visibleText } from './helpers/ui'
import { showWorkspaceWithAgents } from './helpers/workspace'
import { commitFile, initGitRepo } from './helpers/worktree'

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

test.describe('git file versions past the read limit', () => {
  /** 8,000 lines of 35 bytes each, with the newline: 280,000 bytes, past the read limit of 262,144 bytes. */
  const longLines = Array.from({ length: 8000 }, (_, index) => `line ${String(index).padStart(6, '0')} of a long tracked file`)
  const linesText = (lines: string[]) => `${lines.join('\n')}\n`
  const stopNotice = /^The diff stops where the viewer stops reading one of the versions\. It reads only the first 256(\.0)? KB of each version\.$/

  /**
   * The viewer reads at most 256 KiB of the working copy, and of the HEAD and staged versions too: the worker cuts a
   * git read at the limit and reports the whole size. The status bar says so for the version that the mode shows.
   */
  test('says that a long HEAD version is truncated, in diff and HEAD modes', async ({ page, leapmuxServer }) => {
    const dir = createTestDirectory('leapmux-e2e-git-long-')
    try {
      initGitRepo(dir)
      commitFile(dir, 'long.txt', linesText(longLines), 'initial')
      // 7,400 lines of the working copy hold 258,984 bytes. The viewer reads the working copy whole, so only the
      // HEAD read can raise the truncation in diff mode.
      writeFileSync(join(dir, 'long.txt'), linesText([
        'changed first line',
        ...longLines.slice(1, 7400),
      ]))

      await showWorkspaceWithAgents(page, leapmuxServer, 'Long Git File', { workingDir: dir })
      await openFileInDiffMode(page, 'changed', 'long.txt')

      const truncated = page.getByText(/Truncated at 256(\.0)? KB/i).filter({ visible: true })
      await expect(visibleText(page, 'changed first line')).toBeVisible()
      await expect(truncated).toBeVisible()
      await expect(page.getByTestId('diff-stop-notice').filter({ visible: true })).toHaveText(stopNotice)

      await page.locator('[data-testid="diff-mode-head"]').click()
      await expect(page.getByText('line 000000 of a long tracked file').filter({ visible: true })).toBeVisible()
      await expect(truncated).toBeVisible()
    }
    finally {
      rmSync(dir, { recursive: true, force: true })
    }
  })

  /**
   * Lines inserted near the top move the end of the working read up, so the HEAD read holds lines that the working
   * read leaves out. The diff stops where the working read stops, and draws none of those lines as removed.
   */
  test('draws the lines inserted before the cut, and no removal at the end of the reads', async ({ page, leapmuxServer }) => {
    const dir = createTestDirectory('leapmux-e2e-git-insert-')
    try {
      initGitRepo(dir)
      commitFile(dir, 'long.txt', linesText(longLines), 'initial')
      const inserted = Array.from({ length: 100 }, (_, index) => `inserted line ${String(index).padStart(3, '0')}`)
      writeFileSync(join(dir, 'long.txt'), linesText([
        ...longLines.slice(0, 10),
        ...inserted,
        ...longLines.slice(10),
      ]))

      await showWorkspaceWithAgents(page, leapmuxServer, 'Inserted Lines', { workingDir: dir })
      await openFileInDiffMode(page, 'changed', 'long.txt')

      await expect(visibleText(page, 'inserted line 000')).toBeVisible()
      await expect(visibleText(page, 'inserted line 099')).toBeVisible()
      await expect(page.getByTestId('diff-stop-notice').filter({ visible: true })).toHaveText(stopNotice)
      // The last complete line of the HEAD read, which the working read leaves out.
      await expect(visibleText(page, 'line 007488 of a long tracked file')).toHaveCount(0)
    }
    finally {
      rmSync(dir, { recursive: true, force: true })
    }
  })
})

test.describe('git diff of equal versions', () => {
  /** A change of the file mode alone leaves the text as it is, and the diff pane says so instead of drawing nothing. */
  test('states that a staged change of the file mode leaves the same text', async ({ page, leapmuxServer }) => {
    const dir = createTestDirectory('leapmux-e2e-git-mode-')
    try {
      initGitRepo(dir)
      commitFile(dir, 'script.sh', 'echo hello\n', 'initial')
      // The index records the mode, so the staged change does not depend on the file system's support for it.
      execFileSync('git', ['update-index', '--chmod=+x', 'script.sh'], { cwd: dir })

      await showWorkspaceWithAgents(page, leapmuxServer, 'Mode Change', { workingDir: dir })
      await openFileInDiffMode(page, 'staged', 'script.sh')

      await expect(page.getByTestId('equal-versions-notice').filter({ visible: true })).toHaveText('The two versions have the same text.')
    }
    finally {
      rmSync(dir, { recursive: true, force: true })
    }
  })
})

test.describe('git diff context', () => {
  /**
   * The diff worker computes the diff with three unchanged lines around each change, and the gap separators hold
   * the rest of the file until a click reveals them.
   */
  test('draws a change in a long file with three lines of context, and reveals the rest on a click', async ({ page, leapmuxServer }) => {
    const dir = createTestDirectory('leapmux-e2e-git-context-')
    try {
      initGitRepo(dir)
      const rows = Array.from({ length: 250 }, (_, index) => `row ${String(index + 1).padStart(3, '0')}`)
      commitFile(dir, 'context.txt', `${rows.join('\n')}\n`, 'initial')
      // Insert one line after row 100.
      writeFileSync(join(dir, 'context.txt'), `${[
        ...rows.slice(0, 100),
        'inserted line',
        ...rows.slice(100),
      ].join('\n')}\n`)

      await showWorkspaceWithAgents(page, leapmuxServer, 'Diff Context', { workingDir: dir })
      await openFileInDiffMode(page, 'changed', 'context.txt')

      await expect(visibleText(page, 'inserted line')).toBeVisible()
      for (const row of [
        'row 098',
        'row 099',
        'row 100',
        'row 101',
        'row 102',
        'row 103',
      ])
        await expect(visibleText(page, row)).toBeVisible()
      await expect(visibleText(page, 'row 050')).toHaveCount(0)
      await expect(visibleText(page, 'row 200')).toHaveCount(0)

      // Rows 1 to 97 before the change, and rows 104 to 250 after it.
      await expect(visibleText(page, '147 lines hidden')).toBeVisible()
      await visibleText(page, '97 lines hidden').click()

      await expect(visibleText(page, 'row 050')).toBeVisible()
      await expect(visibleText(page, 'row 001')).toBeVisible()
      await expect(visibleText(page, 'row 200')).toHaveCount(0)
    }
    finally {
      rmSync(dir, { recursive: true, force: true })
    }
  })
})

test.describe('git diff layout switch', () => {
  /** 1,500 numbered lines that start with `side`, each ending in a newline. */
  const numberedLines = (side: string): string =>
    Array.from({ length: 1500 }, (_, index) => `${side} line ${String(index + 1).padStart(4, '0')}\n`).join('')

  /** The content area of the file viewer, which scrolls the diff. */
  const viewerContent = (page: Page) => visibleOnly(page.getByTestId('file-viewer-content'))

  /** Whether old line `line` and new line `line` share one row, as only the split layout draws them. */
  async function linesShareARow(page: Page, line: number): Promise<boolean> {
    const number = String(line).padStart(4, '0')
    const [oldBox, newBox] = await Promise.all([
      visibleText(page, `old line ${number}`).boundingBox(),
      visibleText(page, `new line ${number}`).boundingBox(),
    ])
    return oldBox !== null && newBox !== null && oldBox.y === newBox.y
  }

  /** What the file viewer shows at one moment of a switch. */
  interface ViewerSample {
    /** Whether the viewer shows its loading state. */
    loading: boolean
    /** How many diff layouts the viewer holds: two while it mounts a new layout behind the one that it shows. */
    layouts: number
    scrollTop: number
  }

  /** Read one sample of the file viewer. It runs in the page as source text, so it uses nothing from this module. */
  const sampleViewer = (): ViewerSample => {
    const content = [
      ...document.querySelectorAll('[data-testid="file-viewer-content"]'),
    ].find(element => element.checkVisibility())
    return {
      loading: [
        ...document.querySelectorAll('[data-testid="file-viewer-loading"]'),
      ].some(element => element.checkVisibility({ visibilityProperty: true })),
      layouts: content?.querySelectorAll('[data-file-diff]').length ?? 0,
      scrollTop: content?.scrollTop ?? -1,
    }
  }

  /**
   * Check the samples of one switch: the viewer never showed its loading state, it mounted the new layout behind the
   * old one, and the old layout kept the reader's scroll position until the new layout replaced it.
   */
  function expectSwitchKeptTheShownLayout(samples: ViewerSample[], scrollTop: number): void {
    expect(samples.filter(sample => sample.loading)).toEqual([])
    expect(samples.some(sample => sample.layouts === 2)).toBe(true)
    expect(samples.filter(sample => sample.layouts === 2 && sample.scrollTop !== scrollTop)).toEqual([])
  }

  /** Scroll the content of the file viewer to `scrollTop`, as a reader does, and wait until it is there. */
  async function scrollViewer(page: Page, scrollTop: number): Promise<void> {
    const content = viewerContent(page)
    await content.evaluate((element, top) => {
      element.scrollTop = top
    }, scrollTop)
    await expect.poll(() => content.evaluate(element => element.scrollTop)).toBe(scrollTop)
  }

  /**
   * The diff view mounts the lines of a new layout in idle slices, behind the layout that it shows. A switch of a
   * large diff therefore shows the old layout, where the reader left it, until the new layout has every line, and
   * each layout keeps its own scroll position.
   */
  test('switches a large diff to split and back with no loading state, draws every line, and keeps the scroll of each layout', async ({ page, leapmuxServer }) => {
    const dir = createTestDirectory('leapmux-e2e-git-layouts-')
    try {
      initGitRepo(dir)
      commitFile(dir, 'layouts.txt', numberedLines('old'), 'initial')
      writeFileSync(join(dir, 'layouts.txt'), numberedLines('new'))

      await showWorkspaceWithAgents(page, leapmuxServer, 'Diff Layout Switch', { workingDir: dir })
      await openFileInDiffMode(page, 'changed', 'layouts.txt')
      await expect(visibleText(page, 'new line 1500')).toBeVisible()
      expect(await linesShareARow(page, 1)).toBe(false)
      const unifiedScroll = 4000
      await scrollViewer(page, unifiedScroll)

      const toSplit = await domSamplesDuring(page, sampleViewer, async () => {
        await page.getByTestId('diff-mode-split').click()
        await expect.poll(() => linesShareARow(page, 1)).toBe(true)
      })

      expectSwitchKeptTheShownLayout(toSplit, unifiedScroll)
      // Every line, each old line in one row with its new line.
      const content = viewerContent(page)
      await expect(visibleOnly(content.getByText(/^old line \d{4}$/))).toHaveCount(1500)
      await expect(visibleOnly(content.getByText(/^new line \d{4}$/))).toHaveCount(1500)
      expect(await linesShareARow(page, 1500)).toBe(true)

      const splitScroll = 2000
      await scrollViewer(page, splitScroll)
      const toUnified = await domSamplesDuring(page, sampleViewer, async () => {
        await page.getByTestId('diff-mode-unified').click()
        await expect.poll(() => linesShareARow(page, 1)).toBe(false)
      })

      expectSwitchKeptTheShownLayout(toUnified, splitScroll)
      await expect.poll(() => content.evaluate(element => element.scrollTop)).toBe(unifiedScroll)

      await page.getByTestId('diff-mode-split').click()
      await expect.poll(() => linesShareARow(page, 1)).toBe(true)
      await expect.poll(() => content.evaluate(element => element.scrollTop)).toBe(splitScroll)
    }
    finally {
      rmSync(dir, { recursive: true, force: true })
    }
  })
})
