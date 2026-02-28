import process from 'node:process'
import { expect, test } from './fixtures'
import {
  createWorkspaceViaAPI,
  deleteWorkspaceViaAPI,
  loginViaToken,
  waitForWorkspaceReady,
} from './helpers'

test.describe('DirectoryTree', () => {
  test('root directory is visible and collapsible', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'Tree Root Test', adminOrgId, process.cwd())
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // The root node should be visible
      const rootNode = page.locator('[data-testid="tree-root-node"]')
      await expect(rootNode).toBeVisible({ timeout: 15_000 })

      // Children should be visible (root starts expanded)
      await expect(page.getByText('package.json')).toBeVisible({ timeout: 15_000 })

      // Click root to collapse
      await rootNode.click()

      // Children should be hidden
      await expect(page.getByText('package.json')).not.toBeVisible()

      // Click root to expand again
      await rootNode.click()

      // Children should reappear
      await expect(page.getByText('package.json')).toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('directory context menu shows 4 items including terminal', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'Dir Menu Test', adminOrgId, process.cwd())
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Wait for root node to appear
      const rootNode = page.locator('[data-testid="tree-root-node"]')
      await expect(rootNode).toBeVisible({ timeout: 15_000 })

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
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'File Menu Test', adminOrgId, process.cwd())
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Wait for the file tree to load
      await expect(page.getByText('package.json')).toBeVisible({ timeout: 15_000 })

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
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'Open Terminal Test', adminOrgId, process.cwd())
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Wait for root node
      const rootNode = page.locator('[data-testid="tree-root-node"]')
      await expect(rootNode).toBeVisible({ timeout: 15_000 })

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
      await expect(terminalTab).toBeVisible({ timeout: 10_000 })
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('copy path copies absolute path to clipboard', async ({ page, context, leapmuxServer }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'Copy Path Test', adminOrgId, process.cwd())
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Wait for file tree
      await expect(page.getByText('package.json')).toBeVisible({ timeout: 15_000 })

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
      expect(clipboardText).toMatch(/^\//)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('expand state persists across tab switches', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'State Persist Test', adminOrgId, process.cwd())
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Wait for tree to load and ensure root is expanded (default)
      const rootNode = page.locator('[data-testid="tree-root-node"]')
      await expect(rootNode).toBeVisible({ timeout: 15_000 })
      await expect(page.getByText('package.json')).toBeVisible({ timeout: 15_000 })

      // Collapse the root
      await rootNode.click()
      await expect(page.getByText('package.json')).not.toBeVisible()

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

      // Root should still be collapsed (state persisted via sessionStorage)
      await expect(rootNode).toBeVisible()
      await expect(page.getByText('package.json')).not.toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })
})
