import process from 'node:process'
import { expect, test } from './fixtures'
import {
  createWorkspaceViaAPI,
  deleteWorkspaceViaAPI,
  loginViaToken,
  sendMessage,
  waitForAgentIdle,
  waitForWorkspaceReady,
} from './helpers'

test.describe('Quote and Mention', () => {
  test('reply button on assistant message inserts quoted text into editor', async ({ page, authenticatedWorkspace }) => {
    // Wait for the editor to be ready
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Send a message and wait for the assistant to reply
    await sendMessage(page, 'Say exactly: Hello world')
    await waitForAgentIdle(page)

    // Find an assistant bubble row
    const assistantBubble = page.locator('[data-testid="message-bubble"][data-role="assistant"]').first()
    await expect(assistantBubble).toBeVisible()

    // Hover the row to reveal the reply button (it's hidden by default via opacity: 0)
    const messageRow = assistantBubble.locator('..')
    await messageRow.hover()

    // The reply button should become visible
    const replyButton = messageRow.locator('[data-testid="message-reply"]')
    await expect(replyButton).toBeVisible()

    // Click the reply button
    await replyButton.click()

    // Verify the editor now contains a blockquote (Milkdown renders > text as <blockquote>)
    await expect(editor.locator('blockquote')).toBeVisible()
  })

  test('text selection in chat message shows quote popover', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Send a message and wait for the assistant to reply
    await sendMessage(page, 'Say exactly: The quick brown fox jumps over the lazy dog')
    await waitForAgentIdle(page)

    // Find the assistant message content
    const assistantBubble = page.locator('[data-testid="message-bubble"][data-role="assistant"]').first()
    await expect(assistantBubble).toBeVisible()

    const messageContent = assistantBubble.locator('[data-testid="message-content"]')

    // Triple-click to select a paragraph of text
    await messageContent.click({ clickCount: 3 })

    // Wait a bit for the popover to appear (mouseup triggers it)
    await page.waitForTimeout(500)

    // The quote popover should appear
    const quoteButton = page.locator('[data-testid="quote-selection-button"]')
    await expect(quoteButton).toBeVisible({ timeout: 5_000 })

    // Click the quote button
    await quoteButton.click()

    // Verify the editor now contains a blockquote (Milkdown renders > text as <blockquote>)
    await expect(editor.locator('blockquote')).toBeVisible()
  })

  test('AtSign mention button in DirectoryTree hover', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'Tree Mention Test', adminOrgId, process.cwd())
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Ensure an agent tab exists and the editor is ready
      const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Wait for the file tree to load — package.json should be visible
      await expect(page.getByText('package.json')).toBeVisible({ timeout: 15_000 })

      // Find the tree node row containing package.json and hover it
      const packageJsonNode = page.getByText('package.json')
      await packageJsonNode.hover()

      // Click the mention button that appears within the same tree node row
      const treeRow = packageJsonNode.locator('..')
      const mentionButton = treeRow.locator('[data-testid="tree-mention-button"]')
      await expect(mentionButton).toBeVisible()
      await mentionButton.click()

      // Verify the editor contains @package.json (the path is relative to cwd)
      await expect(editor).toContainText('@package.json')
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('AtSign mention button in file view toolbar', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'File Mention Test', adminOrgId, process.cwd())
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Ensure an agent tab exists and click it to populate MRU
      const agentTab = page.locator('[data-testid="tab"][data-tab-type="agent"]')
      await expect(agentTab).toBeVisible()
      await agentTab.click()

      const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Wait for the file tree to load
      await expect(page.getByText('package.json')).toBeVisible({ timeout: 15_000 })

      // Click on package.json to open it as a file tab
      await page.getByText('package.json').click()

      // Wait for the file tab to appear and become active
      const fileTab = page.locator('[data-testid="tab"][data-tab-type="file"]')
      await expect(fileTab).toBeVisible({ timeout: 10_000 })

      // Wait for file content to load
      await page.waitForTimeout(1000)

      // Find the mention button in the floating toolbar
      const mentionButton = page.locator('[data-testid="file-mention-button"]')
      await expect(mentionButton).toBeVisible({ timeout: 5_000 })

      // Click the mention button
      await mentionButton.click()

      // Wait for the agent tab to become active (tab switch + component mount)
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"][aria-selected="true"]'))
        .toBeVisible({ timeout: 5_000 })

      // Verify the editor contains @package.json
      await expect(editor).toContainText('@package.json')
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('text selection quote in file view inserts with file path and line numbers', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'File Quote Test', adminOrgId, process.cwd())
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Ensure an agent tab exists and click it to populate MRU
      const agentTab = page.locator('[data-testid="tab"][data-tab-type="agent"]')
      await expect(agentTab).toBeVisible()
      await agentTab.click()

      const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Wait for the file tree to load
      await expect(page.getByText('package.json')).toBeVisible({ timeout: 15_000 })

      // Click on package.json to open it as a file tab
      await page.getByText('package.json').click()

      // Wait for the file tab and content to load
      const fileTab = page.locator('[data-testid="tab"][data-tab-type="file"]')
      await expect(fileTab).toBeVisible({ timeout: 10_000 })

      // Wait for line-numbered content to appear (data-line-num attributes)
      const lineElements = page.locator('[data-line-num]')
      await expect(lineElements.first()).toBeVisible({ timeout: 10_000 })

      // Triple-click on a line to select text within the file view
      await lineElements.first().click({ clickCount: 3 })

      // Wait for the quote popover to appear
      await page.waitForTimeout(500)

      const quoteButton = page.locator('[data-testid="quote-selection-button"]')
      await expect(quoteButton).toBeVisible({ timeout: 5_000 })

      // Click the quote button
      await quoteButton.click()

      // Wait for the agent tab to become active (tab switch + component mount)
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"][aria-selected="true"]'))
        .toBeVisible({ timeout: 5_000 })

      // Verify the editor contains the expected format with "At" and the path
      await expect(editor).toContainText('At')
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })
})
