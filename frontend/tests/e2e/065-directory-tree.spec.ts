import { rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { frontendRoot } from '~/test-support/sourceTree'
import { expect, test } from './fixtures'
import { createTestDirectory } from './helpers/runDirectory'
import { agentTabs, clickTreeContextItem, openTreeContextMenu, terminalTabs, treeRow, treeRowNames, waitForFilesSortOrder } from './helpers/ui'
import { showWorkspaceWithAgents } from './helpers/workspace'
import { waitForAgentStartupViaAPI } from './helpers/worktree'

const ABSOLUTE_PATH_RE = /^\//

test.describe('DirectoryTree', () => {
  // Most cases read the tree of the frontend directory. A case that needs a
  // directory of its own creates its workspace with `showWorkspaceWithAgents`.
  test.use({ agentWorkingDir: frontendRoot })

  test('root directory is always visible and expanded', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace

    // The root node should be visible
    const rootNode = page.locator('[data-testid="tree-root-node"]')
    await expect(rootNode).toBeVisible()

    // Children should be visible (root is always expanded)
    await expect(treeRow(page, 'package.json')).toBeVisible()

    // Clicking root should NOT collapse it (root is uncollapsible)
    await rootNode.click()
    await expect(treeRow(page, 'package.json')).toBeVisible()
  })

  test('directory context menu shows the info block and the terminal item', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace

    // Wait for root node to appear
    const rootNode = page.locator('[data-testid="tree-root-node"]')
    await expect(rootNode).toBeVisible()

    // Hover the root node and open context menu
    await openTreeContextMenu(rootNode)

    // Every directory item should be visible (use :visible to scope to the open popover).
    // The info block leads: a directory reports a modification time but no size.
    const dirInfo = page.locator('[data-testid="tree-info-button"]:visible')
    await expect(dirInfo).toBeVisible()
    await expect(dirInfo).toContainText('Modified:')
    await expect(dirInfo).not.toContainText('Size:')
    await expect(page.locator('[data-testid="tree-mention-button"]:visible')).toBeVisible()
    await expect(page.locator('[data-testid="tree-open-terminal-button"]:visible')).toBeVisible()
    await expect(page.locator('[data-testid="tree-copy-path-button"]:visible')).toBeVisible()
    await expect(page.locator('[data-testid="tree-copy-relative-path-button"]:visible')).toBeVisible()
  })

  test('file context menu shows size and modified but no terminal item', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace

    // Wait for the file tree to load
    await expect(treeRow(page, 'package.json')).toBeVisible()

    // Hover on package.json file and open context menu. We anchor on the
    // tree-row testid because the label is now nested inside a Tooltip
    // span pair, so `.locator('..')` from the text no longer lands on the
    // row hosting the context button.
    await openTreeContextMenu(treeRow(page, 'package.json'))

    // Info block (size + modified), mention, copy path, copy relative path — but NOT terminal
    const fileInfo = page.locator('[data-testid="tree-info-button"]:visible')
    await expect(fileInfo).toContainText('Size:')
    await expect(fileInfo).toContainText('Modified:')
    await expect(page.locator('[data-testid="tree-mention-button"]:visible')).toBeVisible()
    await expect(page.locator('[data-testid="tree-copy-path-button"]:visible')).toBeVisible()
    await expect(page.locator('[data-testid="tree-copy-relative-path-button"]:visible')).toBeVisible()
    await expect(page.locator('[data-testid="tree-open-terminal-button"]:visible')).toHaveCount(0)
  })

  test('open terminal tab from directory context menu', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace

    // Wait for root node
    const rootNode = page.locator('[data-testid="tree-root-node"]')
    await expect(rootNode).toBeVisible()

    // Open the root directory's context menu and click "Open a terminal tab
    // here" as one retried unit -- same detach hazard as the copy-path test.
    await clickTreeContextItem(rootNode, 'tree-open-terminal-button')

    // A terminal tab should appear
    await expect(terminalTabs(page)).toBeVisible()
  })

  test('copy path copies absolute path to clipboard', async ({ page, context, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])

    // Wait for file tree
    await expect(treeRow(page, 'package.json')).toBeVisible()

    // Open the context menu on package.json and click "Copy path" as ONE
    // retried unit (anchored to tree-row testid; see earlier note about the
    // Tooltip span wrap). Opening and clicking as two separate steps lets a
    // sidebar re-render between them detach the item mid-click -- which is
    // what "element was detached from the DOM" was reporting here.
    await clickTreeContextItem(treeRow(page, 'package.json'), 'tree-copy-path-button')

    // Clipboard should contain the absolute path (ends with /package.json)
    const clipboardText = await page.evaluate(() => navigator.clipboard.readText())
    expect(clipboardText).toContain('package.json')
    expect(clipboardText).toMatch(ABSOLUTE_PATH_RE)
  })

  test('collapsing a directory does not scroll the tree', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace

    // Wait for tree to load
    const rootNode = page.locator('[data-testid="tree-root-node"]')
    await expect(rootNode).toBeVisible()
    await expect(treeRow(page, 'package.json')).toBeVisible()

    // Expand "src" to add more items to the tree. `exact`, so a longer name
    // that contains "src" cannot answer for it.
    const srcNode = treeRow(page, 'src', { exact: true })
    await expect(srcNode).toBeVisible()
    await srcNode.click()
    await page.waitForTimeout(500)

    // Select a file to change selectedPath away from "src".
    // This is needed because clicking src again to collapse only triggers
    // the scroll-on-select effect when selectedPath actually changes.
    const fileNode = treeRow(page, 'package.json')
    await fileNode.click()
    await page.waitForTimeout(200)

    // Find the tree scroll container (first ancestor with overflow: auto)
    // and constrain its height to force it to be scrollable.
    const scrollContainerHandle = await page.evaluateHandle(() => {
      const node = document.querySelector('[data-testid="tree-root-node"]')
      if (!node)
        return null
      let el: Element | null = node.parentElement
      while (el) {
        const style = window.getComputedStyle(el)
        if (style.overflow === 'auto' || style.overflowY === 'auto')
          return el
        el = el.parentElement
      }
      return null
    })

    const isNull = await scrollContainerHandle.evaluate(el => el === null)
    expect(isNull).toBe(false)

    // Force the container to a small fixed height so tree content overflows
    await scrollContainerHandle.evaluate((el) => {
      if (el)
        (el as HTMLElement).style.maxHeight = '150px'
    })
    await page.waitForTimeout(100)

    // Verify the container is now scrollable
    const scrollable = await scrollContainerHandle.evaluate(
      el => el ? el.scrollHeight > el.clientHeight : false,
    )
    expect(scrollable).toBe(true)

    // Scroll down so "src" is partially visible near the bottom
    await scrollContainerHandle.evaluate((el) => {
      if (el)
        (el as HTMLElement).scrollTop = Math.min(50, el.scrollHeight - el.clientHeight)
    })
    await page.waitForTimeout(100)

    const scrollTopBefore = await scrollContainerHandle.evaluate(
      el => el ? (el as HTMLElement).scrollTop : 0,
    )
    expect(scrollTopBefore).toBeGreaterThan(0)

    // Collapse "src" — should NOT change scroll position.
    // selectedPath changes from the file to src, which would trigger
    // the scroll-on-select effect without the fix.
    // Use dispatchEvent instead of Playwright's click() to avoid
    // auto-scroll-into-view which would change scrollTop before the
    // toggle handler captures it. The row itself carries the click handler.
    await srcNode.dispatchEvent('click')
    // Wait for rAF (the scroll-on-select effect fires in requestAnimationFrame)
    await page.waitForTimeout(300)

    const scrollTopAfter = await scrollContainerHandle.evaluate(
      el => el ? (el as HTMLElement).scrollTop : 0,
    )
    expect(scrollTopAfter).toBe(scrollTopBefore)
  })

  test('collapse all collapses every expanded directory', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace

    // Wait for tree to load — root is expanded by default
    const rootNode = page.locator('[data-testid="tree-root-node"]')
    await expect(rootNode).toBeVisible()
    await expect(treeRow(page, 'package.json')).toBeVisible()

    // Expand "src" directory (child of root = frontend/)
    const srcNode = treeRow(page, 'src', { exact: true })
    await expect(srcNode).toBeVisible()
    await srcNode.click()
    await page.waitForTimeout(500)

    // "components" should now be visible (child of src)
    const componentsNode = treeRow(page, 'components', { exact: true })
    await expect(componentsNode).toBeVisible()

    // Click collapse all button
    await page.locator('[data-testid="files-collapse-all"]').click()
    // Wait for collapse animation (150ms transition)
    await page.waitForTimeout(300)

    // Root should still be expanded — root-level items still visible
    await expect(treeRow(page, 'package.json')).toBeVisible()
    // "src" is a root child, so it should still be visible
    await expect(srcNode).toBeVisible()
    // But "components" (child of src) should be hidden because src is collapsed
    await expect(componentsNode).not.toBeVisible()
  })

  test('large directory shows truncation indicator', async ({ page, leapmuxServer }) => {
    // Create a temp directory with more than 256 entries
    const largeDir = createTestDirectory('large-directory-')
    const totalFiles = 300
    for (let i = 0; i < totalFiles; i++) {
      writeFileSync(join(largeDir, `file${String(i).padStart(3, '0')}.txt`), '')
    }

    try {
      await showWorkspaceWithAgents(page, leapmuxServer, 'Truncation Test', { workingDir: largeDir })

      // The root node should be visible
      const rootNode = page.locator('[data-testid="tree-root-node"]')
      await expect(rootNode).toBeVisible()

      // The truncation indicator should appear
      const truncationIndicator = page.getByText('entries, listing truncated')
      await expect(truncationIndicator).toBeVisible()
    }
    finally {
      rmSync(largeDir, { recursive: true, force: true })
    }
  })

  test('expand state persists across tab switches', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace

    // Wait for tree to load
    const rootNode = page.locator('[data-testid="tree-root-node"]')
    await expect(rootNode).toBeVisible()
    await expect(treeRow(page, 'package.json')).toBeVisible()

    // Expand "src" directory
    const srcNode = treeRow(page, 'src', { exact: true })
    await expect(srcNode).toBeVisible()
    await srcNode.click()
    await page.waitForTimeout(500)

    // "components" should now be visible (child of src)
    const componentsNode = treeRow(page, 'components', { exact: true })
    await expect(componentsNode).toBeVisible()

    // Collapse "src"
    await srcNode.click()
    await expect(componentsNode).not.toBeVisible()

    // Switch to a terminal tab (if exists) or create one
    const terminalTab = terminalTabs(page)
    const hasTerminal = await terminalTab.count() > 0
    if (hasTerminal) {
      await terminalTab.first().click()
    }

    // Switch back to agent tab
    await agentTabs(page).first().click()
    await page.waitForTimeout(500)

    // "src" should still be collapsed (state persisted via sessionStorage)
    await expect(srcNode).toBeVisible()
    await expect(componentsNode).not.toBeVisible()
  })

  test('sort menu reorders the tree and the choice survives a reload', async ({ page, leapmuxServer }) => {
    const { adminUserId, workerId } = leapmuxServer
    // A directory whose size order differs from its name order, so an
    // assertion on the row order cannot pass with the sort key ignored.
    const sortDir = createTestDirectory('sort-directory-')
    writeFileSync(join(sortDir, 'apple.txt'), 'x'.repeat(900))
    writeFileSync(join(sortDir, 'banana.txt'), 'x'.repeat(10))
    writeFileSync(join(sortDir, 'cherry.txt'), 'x'.repeat(100))

    try {
      await showWorkspaceWithAgents(page, leapmuxServer, 'Sort Test', { workingDir: sortDir })

      const names = treeRowNames(page)
      await expect(names).toHaveText(['apple.txt', 'banana.txt', 'cherry.txt'])

      // Criterion and direction live in one popover, so both are reachable
      // without reopening it.
      await page.locator('[data-testid="files-sort-toggle"]:visible').click()
      await page.locator('[data-testid="files-sort-key-size"]:visible').click()
      await page.locator('[data-testid="files-sort-direction-desc"]:visible').click()
      await expect(names).toHaveText(['apple.txt', 'cherry.txt', 'banana.txt'])

      await page.keyboard.press('Escape')
      // The choice is on screen well before it is on disk: it goes through a
      // coalescing write-behind queue, and reloading without waiting drops it
      // every time. Assert the state the reload depends on, not a delay.
      await waitForFilesSortOrder(page, adminUserId, workerId, { key: 'size', direction: 'desc' })
      await page.reload()
      await expect(names).toHaveText(['apple.txt', 'cherry.txt', 'banana.txt'])
    }
    finally {
      rmSync(sortDir, { recursive: true, force: true })
    }
  })

  test('the sidebar tree has no path input', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    await expect(page.locator('[data-testid="tree-root-node"]')).toBeVisible()

    // The box belongs to the dialog picker, where a typed path retargets the
    // tree. The sidebar shows the active tab's working directory, which the
    // user cannot retarget by typing.
    await expect(page.getByPlaceholder('Enter path...')).toHaveCount(0)
  })

  /**
   * The tree is the clean control for the right-click path: its rows carry no
   * drag, so nothing else competes for the press.
   */
  test('right-click opens a row menu without selecting the row', async ({ page, leapmuxServer, authenticatedWorkspace }) => {
    // The Files tree waits for the active agent: while the agent starts, a
    // startup spinner takes its place, and the tree builds its rows again when
    // the agent leaves STARTING. A menu that opened on the old rows closes with
    // them. So the right-click waits until the Worker reports the agent started.
    const { hubUrl, adminToken, workerId } = leapmuxServer
    await waitForAgentStartupViaAPI(hubUrl, adminToken, workerId, authenticatedWorkspace.workspaceId)

    const row = treeRow(page, 'package.json')
    await expect(row).toBeVisible()

    // Each row renders its own menu inside the row, so look up an item inside
    // the row whose menu the test means. A page-wide `:visible` lookup also
    // finds a menu that just closed: Oat fades a closed popover out for up to
    // 150ms and keeps it laid out for that time, and Playwright counts a
    // laid-out element as visible at any opacity.
    const rowCopyPath = row.getByTestId('tree-copy-path-button')
    await row.click({ button: 'right' })
    await expect(rowCopyPath).toBeVisible()
    await expect(row).toHaveAttribute('aria-selected', 'false')

    await page.keyboard.press('Escape')
    await expect(rowCopyPath).toBeHidden()

    // The root row owns its own menu, so a right-click there is not the same
    // element's menu re-anchored.
    const rootRow = page.locator('[data-testid="tree-root-node"]:visible')
    await rootRow.click({ button: 'right' })
    await expect(rootRow.getByTestId('tree-copy-path-button')).toBeVisible()
    await expect(rowCopyPath).toBeHidden()
  })
})
