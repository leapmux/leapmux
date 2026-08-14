import { mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path, { join } from 'node:path'
import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { clickTreeContextItem, loginViaToken, openTreeContextMenu, openWorkspace, treeRow, treeRowNames } from './helpers/ui'

const frontendDir = path.resolve(import.meta.dirname, '../..')
const ABSOLUTE_PATH_RE = /^\//

test.describe('DirectoryTree', () => {
  test('root directory is always visible and expanded', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Tree Root Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      // The root node should be visible
      const rootNode = page.locator('[data-testid="tree-root-node"]')
      await expect(rootNode).toBeVisible()

      // Children should be visible (root is always expanded)
      await expect(treeRow(page, 'package.json')).toBeVisible()

      // Clicking root should NOT collapse it (root is uncollapsible)
      await rootNode.click()
      await expect(treeRow(page, 'package.json')).toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('directory context menu shows the info block and the terminal item', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Dir Menu Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      // Wait for root node to appear
      const rootNode = page.locator('[data-testid="tree-root-node"]')
      await expect(rootNode).toBeVisible()

      // Hover the root node and open context menu
      await openTreeContextMenu(page, rootNode)

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
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('file context menu shows size and modified but no terminal item', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'File Menu Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      // Wait for the file tree to load
      await expect(treeRow(page, 'package.json')).toBeVisible()

      // Hover on package.json file and open context menu. We anchor on the
      // tree-row testid because the label is now nested inside a Tooltip
      // span pair, so `.locator('..')` from the text no longer lands on the
      // row hosting the context button.
      await openTreeContextMenu(page, treeRow(page, 'package.json'))

      // Info block (size + modified), mention, copy path, copy relative path — but NOT terminal
      const fileInfo = page.locator('[data-testid="tree-info-button"]:visible')
      await expect(fileInfo).toContainText('Size:')
      await expect(fileInfo).toContainText('Modified:')
      await expect(page.locator('[data-testid="tree-mention-button"]:visible')).toBeVisible()
      await expect(page.locator('[data-testid="tree-copy-path-button"]:visible')).toBeVisible()
      await expect(page.locator('[data-testid="tree-copy-relative-path-button"]:visible')).toBeVisible()
      await expect(page.locator('[data-testid="tree-open-terminal-button"]:visible')).toHaveCount(0)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('open terminal tab from directory context menu', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Open Terminal Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      // Wait for root node
      const rootNode = page.locator('[data-testid="tree-root-node"]')
      await expect(rootNode).toBeVisible()

      // Open the root directory's context menu and click "Open a terminal tab
      // here" as one retried unit -- same detach hazard as the copy-path test.
      await clickTreeContextItem(page, rootNode, 'tree-open-terminal-button')

      // A terminal tab should appear
      const terminalTab = page.locator('[data-testid="tab"][data-tab-type="terminal"]')
      await expect(terminalTab).toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('copy path copies absolute path to clipboard', async ({ page, context, leapmuxServer }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Copy Path Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      // Wait for file tree
      await expect(treeRow(page, 'package.json')).toBeVisible()

      // Open the context menu on package.json and click "Copy path" as ONE
      // retried unit (anchored to tree-row testid; see earlier note about the
      // Tooltip span wrap). Opening and clicking as two separate steps lets a
      // sidebar re-render between them detach the item mid-click -- which is
      // what "element was detached from the DOM" was reporting here.
      await clickTreeContextItem(page, treeRow(page, 'package.json'), 'tree-copy-path-button')

      // Clipboard should contain the absolute path (ends with /package.json)
      const clipboardText = await page.evaluate(() => navigator.clipboard.readText())
      expect(clipboardText).toContain('package.json')
      expect(clipboardText).toMatch(ABSOLUTE_PATH_RE)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('collapsing a directory does not scroll the tree', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Collapse Scroll Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      // Wait for tree to load
      const rootNode = page.locator('[data-testid="tree-root-node"]')
      await expect(rootNode).toBeVisible()
      await expect(treeRow(page, 'package.json')).toBeVisible()

      // Expand "src" to add more items to the tree
      const srcNode = page.locator('span:text-is("src")').first()
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
      // toggle handler captures it.
      await srcNode.dispatchEvent('click')
      // Wait for rAF (the scroll-on-select effect fires in requestAnimationFrame)
      await page.waitForTimeout(300)

      const scrollTopAfter = await scrollContainerHandle.evaluate(
        el => el ? (el as HTMLElement).scrollTop : 0,
      )
      expect(scrollTopAfter).toBe(scrollTopBefore)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('collapse all collapses every expanded directory', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    // process.cwd() is the frontend directory in the test runner
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Collapse All Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      // Wait for tree to load — root is expanded by default
      const rootNode = page.locator('[data-testid="tree-root-node"]')
      await expect(rootNode).toBeVisible()
      await expect(treeRow(page, 'package.json')).toBeVisible()

      // Expand "src" directory (child of root = frontend/)
      const srcNode = page.locator('span:text-is("src")').first()
      await expect(srcNode).toBeVisible()
      await srcNode.click()
      await page.waitForTimeout(500)

      // "components" should now be visible (child of src)
      const componentsNode = page.locator('span:text-is("components")').first()
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
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('large directory shows truncation indicator', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    // Create a temp directory with more than 256 entries
    const largeDir = join(tmpdir(), `leapmux-e2e-largedir-${Date.now()}`)
    mkdirSync(largeDir)
    const totalFiles = 300
    for (let i = 0; i < totalFiles; i++) {
      writeFileSync(join(largeDir, `file${String(i).padStart(3, '0')}.txt`), '')
    }

    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Truncation Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, largeDir)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      // The root node should be visible
      const rootNode = page.locator('[data-testid="tree-root-node"]')
      await expect(rootNode).toBeVisible()

      // The truncation indicator should appear
      const truncationIndicator = page.getByText('entries, listing truncated')
      await expect(truncationIndicator).toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
      rmSync(largeDir, { recursive: true, force: true })
    }
  })

  test('expand state persists across tab switches', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'State Persist Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      // Wait for tree to load
      const rootNode = page.locator('[data-testid="tree-root-node"]')
      await expect(rootNode).toBeVisible()
      await expect(treeRow(page, 'package.json')).toBeVisible()

      // Expand "src" directory
      const srcNode = page.locator('span:text-is("src")').first()
      await expect(srcNode).toBeVisible()
      await srcNode.click()
      await page.waitForTimeout(500)

      // "components" should now be visible (child of src)
      const componentsNode = page.locator('span:text-is("components")').first()
      await expect(componentsNode).toBeVisible()

      // Collapse "src"
      await srcNode.click()
      await expect(componentsNode).not.toBeVisible()

      // Switch to a terminal tab (if exists) or create one
      const terminalTab = page.locator('[data-testid="tab"][data-tab-type="terminal"]')
      const hasTerminal = await terminalTab.count() > 0
      if (hasTerminal) {
        await terminalTab.first().click()
      }

      // Switch back to agent tab
      const agentTab = page.locator('[data-testid="tab"][data-tab-type="agent"]')
      await agentTab.first().click()
      await page.waitForTimeout(500)

      // "src" should still be collapsed (state persisted via sessionStorage)
      await expect(srcNode).toBeVisible()
      await expect(componentsNode).not.toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('sort menu reorders the tree and the choice survives a reload', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    // A directory whose size order differs from its name order, so an
    // assertion on the row order cannot pass with the sort key ignored.
    const sortDir = join(tmpdir(), `leapmux-e2e-sortdir-${Date.now()}`)
    mkdirSync(sortDir)
    writeFileSync(join(sortDir, 'apple.txt'), 'x'.repeat(900))
    writeFileSync(join(sortDir, 'banana.txt'), 'x'.repeat(10))
    writeFileSync(join(sortDir, 'cherry.txt'), 'x'.repeat(100))

    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Sort Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, sortDir)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      const names = treeRowNames(page)
      await expect(names).toHaveText(['apple.txt', 'banana.txt', 'cherry.txt'])

      // Criterion and direction live in one popover, so both are reachable
      // without reopening it.
      await page.locator('[data-testid="files-sort-toggle"]:visible').click()
      await page.locator('[data-testid="files-sort-key-size"]:visible').click()
      await page.locator('[data-testid="files-sort-direction-desc"]:visible').click()
      await expect(names).toHaveText(['apple.txt', 'cherry.txt', 'banana.txt'])

      await page.keyboard.press('Escape')
      await page.reload()
      await expect(names).toHaveText(['apple.txt', 'cherry.txt', 'banana.txt'])
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
      rmSync(sortDir, { recursive: true, force: true })
    }
  })

  test('the sidebar tree has no path input', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'No Path Input Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)
      await expect(page.locator('[data-testid="tree-root-node"]')).toBeVisible()

      // The box belongs to the dialog picker, where a typed path retargets the
      // tree. The sidebar shows the active tab's working directory, which the
      // user cannot retarget by typing.
      await expect(page.getByPlaceholder('Enter path...')).toHaveCount(0)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })
})
