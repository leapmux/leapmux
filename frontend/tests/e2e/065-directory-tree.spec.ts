import { rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { frontendRoot } from '~/test-support/sourceTree'
import { expect, test } from './fixtures'
import { settleFrames, settleTransitions } from './helpers/frames'
import { createTestDirectory } from './helpers/runDirectory'
import { agentTabs, clickTreeContextItem, openTreeContextMenu, terminalTabs, treeRow, treeRowNames, waitForFilesSortOrder } from './helpers/ui'
import { waitForAgentStartupViaAPI } from './helpers/workerTabs'
import { showWorkspaceWithAgents } from './helpers/workspace'

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

    // Open the context menu of the package.json row. The locator starts at the
    // tree-row test id, because a Tooltip wraps the label in a pair of spans.
    // A `.locator('..')` from the label text does not reach the row that holds
    // the menu button.
    await openTreeContextMenu(treeRow(page, 'package.json'))

    // The menu of a file holds these items, and no terminal item:
    // - The info block, with the size and the modification time.
    // - The mention item.
    // - The two copy items.
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

    // Open the context menu of the root row and click "Open a terminal tab
    // here" as one retried unit. The item can detach between two separate
    // steps, for the reason that the copy-path test gives.
    await clickTreeContextItem(rootNode, 'tree-open-terminal-button')

    // A terminal tab should appear
    await expect(terminalTabs(page)).toBeVisible()
  })

  test('copy path copies absolute path to clipboard', async ({ page, context, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])

    // Wait for file tree
    await expect(treeRow(page, 'package.json')).toBeVisible()

    // Open the context menu of package.json and click "Copy path" as ONE
    // retried unit. The locator starts at the tree-row test id, for the
    // Tooltip reason that the file context menu test gives. With two separate
    // steps, a re-render of the sidebar between them can detach the item
    // during the click, and Playwright then fails with "element was detached
    // from the DOM".
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

    // The expand and collapse transitions of the rows run inside the tree.
    const tree = page.getByRole('tree', { name: 'Directory tree' })

    // Expand "src" to add more items to the tree. `exact`, so a longer name
    // that contains "src" cannot answer for it.
    const srcNode = treeRow(page, 'src', { exact: true })
    await expect(srcNode).toBeVisible()
    await srcNode.click()
    // The listing of "src" arrived and the row expanded. The tree scrolls the
    // new children into view when the expand transition ends, so the scroll
    // setup below starts after that end.
    await expect(srcNode).toHaveAttribute('aria-expanded', 'true')
    await expect(treeRow(page, 'components', { exact: true })).toBeVisible()
    await settleTransitions(tree)

    // Select a file to change selectedPath away from "src".
    // This is needed because clicking src again to collapse only triggers
    // the scroll-on-select effect when selectedPath actually changes.
    const fileNode = treeRow(page, 'package.json')
    await fileNode.click()
    // The scroll-on-select effect of the file scrolls in the frame after the
    // selection.
    await expect(fileNode).toHaveAttribute('aria-selected', 'true')
    await settleFrames(page)

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
    // The ResizeObserver of the tree sees the new height in the next frame,
    // and it scrolls the selected row back into view in a frame callback. The
    // frames after the change run both, so that scroll cannot land after the
    // scroll position that this test sets below.
    await settleFrames(page)

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
    // The page dispatches the `scroll` event in the next frame.
    await settleFrames(page)

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
    // The collapse and the selection of "src" landed. The scroll-on-select
    // effect runs in the frame after the selection, and the collapse
    // transition ends 150ms later with its `transitionend`. Both are over
    // before the scroll position is read.
    await expect(srcNode).toHaveAttribute('aria-expanded', 'false')
    await expect(srcNode).toHaveAttribute('aria-selected', 'true')
    await settleTransitions(tree)

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
    await expect(srcNode).toHaveAttribute('aria-expanded', 'true')

    // "components" should now be visible (child of src)
    const componentsNode = treeRow(page, 'components', { exact: true })
    await expect(componentsNode).toBeVisible()

    // Click collapse all button
    await page.locator('[data-testid="files-collapse-all"]').click()
    // "src" collapses. Its children stay visible until the 150ms collapse
    // transition ends, so "components" (child of src) is hidden only after
    // that end. The checks of the root-level rows come after it, so they
    // cannot pass before the collapse ended.
    await expect(srcNode).toHaveAttribute('aria-expanded', 'false')
    await expect(componentsNode).not.toBeVisible()

    // Root should still be expanded — root-level items still visible
    await expect(treeRow(page, 'package.json')).toBeVisible()
    // "src" is a root child, so it should still be visible
    await expect(srcNode).toBeVisible()
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

    // Expand "src" directory. The tree shows a directory collapsed by default,
    // so an expanded "src" is the state that a reset tree cannot show.
    const srcNode = treeRow(page, 'src', { exact: true })
    await expect(srcNode).toBeVisible()
    await srcNode.click()
    await expect(srcNode).toHaveAttribute('aria-expanded', 'true')

    // "components" should now be visible (child of src)
    const componentsNode = treeRow(page, 'components', { exact: true })
    await expect(componentsNode).toBeVisible()

    // Switch to a terminal tab. A terminal that the tree opens becomes the
    // active tab.
    await clickTreeContextItem(rootNode, 'tree-open-terminal-button')
    await expect(terminalTabs(page)).toHaveAttribute('aria-selected', 'true')

    // Switch back to agent tab
    const agentTab = agentTabs(page).first()
    await agentTab.click()
    await expect(agentTab).toHaveAttribute('aria-selected', 'true')

    // "src" should still be expanded
    await expect(srcNode).toHaveAttribute('aria-expanded', 'true')
    await expect(componentsNode).toBeVisible()
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
