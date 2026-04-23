import { mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path, { join } from 'node:path'
import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'

const frontendDir = path.resolve(import.meta.dirname, '../..')
const ABSOLUTE_PATH_RE = /^\//

test.describe('DirectoryTree', () => {
  test('root directory is always visible and expanded', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Tree Root Test', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // The root node should be visible
      const rootNode = page.locator('[data-testid="tree-root-node"]')
      await expect(rootNode).toBeVisible()

      // Children should be visible (root is always expanded)
      await expect(page.getByText('package.json')).toBeVisible()

      // Clicking root should NOT collapse it (root is uncollapsible)
      await rootNode.click()
      await expect(page.getByText('package.json')).toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('directory context menu shows 4 items including terminal', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Dir Menu Test', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Wait for root node to appear
      const rootNode = page.locator('[data-testid="tree-root-node"]')
      await expect(rootNode).toBeVisible()

      // Hover the root node and open context menu
      await rootNode.hover()
      const contextButton = rootNode.locator('[data-testid="tree-context-button"]')
      await expect(contextButton).toBeVisible()
      await contextButton.click()

      // All 4 menu items should be visible for a directory (use :visible to scope to the open popover)
      await expect(page.locator('[data-testid="tree-mention-button"]:visible')).toBeVisible()
      await expect(page.locator('[data-testid="tree-open-terminal-button"]:visible')).toBeVisible()
      await expect(page.locator('[data-testid="tree-copy-path-button"]:visible')).toBeVisible()
      await expect(page.locator('[data-testid="tree-copy-relative-path-button"]:visible')).toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('file context menu shows 3 items without terminal', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'File Menu Test', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Wait for the file tree to load
      await expect(page.getByText('package.json')).toBeVisible()

      // Hover on package.json file and open context menu
      const fileNode = page.getByText('package.json')
      await fileNode.hover()
      const treeRow = fileNode.locator('..')
      const contextButton = treeRow.locator('[data-testid="tree-context-button"]')
      await expect(contextButton).toBeVisible()
      await contextButton.click()

      // 3 items: mention, copy path, copy relative path — but NOT terminal
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
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Open Terminal Test', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Wait for root node
      const rootNode = page.locator('[data-testid="tree-root-node"]')
      await expect(rootNode).toBeVisible()

      // Hover and open context menu on the root directory
      await rootNode.hover()
      const contextButton = rootNode.locator('[data-testid="tree-context-button"]')
      await expect(contextButton).toBeVisible()
      await contextButton.click()

      // Click "Open a terminal tab here"
      const terminalButton = page.locator('[data-testid="tree-open-terminal-button"]:visible')
      await expect(terminalButton).toBeVisible()
      await terminalButton.click()

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
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Copy Path Test', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Wait for file tree
      await expect(page.getByText('package.json')).toBeVisible()

      // Open context menu on package.json
      const fileNode = page.getByText('package.json')
      await fileNode.hover()
      const treeRow = fileNode.locator('..')
      const contextButton = treeRow.locator('[data-testid="tree-context-button"]')
      await expect(contextButton).toBeVisible()
      await contextButton.click()

      // Click "Copy path" from the visible dropdown
      const copyPathButton = page.locator('[data-testid="tree-copy-path-button"]:visible')
      await expect(copyPathButton).toBeVisible()
      await copyPathButton.click()

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
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Collapse Scroll Test', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Wait for tree to load
      const rootNode = page.locator('[data-testid="tree-root-node"]')
      await expect(rootNode).toBeVisible()
      await expect(page.getByText('package.json')).toBeVisible()

      // Expand "src" to add more items to the tree
      const srcNode = page.locator('span:text-is("src")').first()
      await expect(srcNode).toBeVisible()
      await srcNode.click()
      await page.waitForTimeout(500)

      // Select a file to change selectedPath away from "src".
      // This is needed because clicking src again to collapse only triggers
      // the scroll-on-select effect when selectedPath actually changes.
      const fileNode = page.getByText('package.json')
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
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    // process.cwd() is the frontend directory in the test runner
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Collapse All Test', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Wait for tree to load — root is expanded by default
      const rootNode = page.locator('[data-testid="tree-root-node"]')
      await expect(rootNode).toBeVisible()
      await expect(page.getByText('package.json')).toBeVisible()

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
      await expect(page.getByText('package.json')).toBeVisible()
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
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    // Create a temp directory with more than 256 entries
    const largeDir = join(tmpdir(), `leapmux-e2e-largedir-${Date.now()}`)
    mkdirSync(largeDir)
    const totalFiles = 300
    for (let i = 0; i < totalFiles; i++) {
      writeFileSync(join(largeDir, `file${String(i).padStart(3, '0')}.txt`), '')
    }

    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Truncation Test', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, largeDir)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

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
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'State Persist Test', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Wait for tree to load
      const rootNode = page.locator('[data-testid="tree-root-node"]')
      await expect(rootNode).toBeVisible()
      await expect(page.getByText('package.json')).toBeVisible()

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
})
