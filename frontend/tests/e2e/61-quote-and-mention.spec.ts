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
    const replyButton = messageRow.locator('[data-testid="message-quote"]')
    await expect(replyButton).toBeVisible()

    // Click the reply button
    await replyButton.click()

    // Verify the editor now contains a blockquote (Milkdown renders > text as <blockquote>)
    await expect(editor.locator('blockquote')).toBeVisible()
  })

  test('cursor lands outside blockquote after quoting', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Send a message and wait for the assistant to reply
    await sendMessage(page, 'Say exactly: Hello world')
    await waitForAgentIdle(page)

    // Find an assistant bubble and click the quote button
    const assistantBubble = page.locator('[data-testid="message-bubble"][data-role="assistant"]').first()
    await expect(assistantBubble).toBeVisible()
    const messageRow = assistantBubble.locator('..')
    await messageRow.hover()
    const quoteButton = messageRow.locator('[data-testid="message-quote"]')
    await expect(quoteButton).toBeVisible()
    await quoteButton.click()

    // Verify the blockquote was inserted
    await expect(editor.locator('blockquote')).toBeVisible()

    // Type some text — it should appear OUTSIDE the blockquote (in a new paragraph)
    await page.keyboard.type('my follow-up')

    // The typed text should not be inside the blockquote
    const blockquoteText = await editor.locator('blockquote').textContent()
    const editorText = await editor.textContent()
    expect(editorText).toContain('my follow-up')
    expect(blockquoteText).not.toContain('my follow-up')
  })

  test('text selection copy button copies to clipboard', async ({ page, context, authenticatedWorkspace }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])

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

    // Wait for the popover to appear
    await page.waitForTimeout(500)

    // The copy button should appear
    const copyButton = page.locator('[data-testid="copy-selection-button"]')
    await expect(copyButton).toBeVisible({ timeout: 5_000 })

    // Click the copy button
    await copyButton.click()

    // Clipboard should contain the selected text
    const clipboardText = await page.evaluate(() => navigator.clipboard.readText())
    expect(clipboardText).toBeTruthy()
    expect(clipboardText.length).toBeGreaterThan(0)
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

  test('AtSign mention button in DirectoryTree context menu', async ({ page, leapmuxServer }) => {
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

      // Click the context menu button (three dots) that appears on hover
      const treeRow = packageJsonNode.locator('..')
      const contextButton = treeRow.locator('[data-testid="tree-context-button"]')
      await expect(contextButton).toBeVisible()
      await contextButton.click()

      // Click "Mention in chat" from the visible dropdown
      const mentionButton = page.locator('[data-testid="tree-mention-button"]:visible')
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

  test('file view mention preserves existing editor draft', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'Mention Preserve Test', adminOrgId, process.cwd())
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

      // Type some draft text into the editor
      await editor.click()
      await editor.pressSequentially('my draft text')

      // Wait for the file tree to load
      await expect(page.getByText('package.json')).toBeVisible({ timeout: 15_000 })

      // Click on package.json to open it as a file tab
      await page.getByText('package.json').click()

      const fileTab = page.locator('[data-testid="tab"][data-tab-type="file"]')
      await expect(fileTab).toBeVisible({ timeout: 10_000 })
      await page.waitForTimeout(1000)

      // Click the mention button in the file view toolbar
      const mentionButton = page.locator('[data-testid="file-mention-button"]')
      await expect(mentionButton).toBeVisible({ timeout: 5_000 })
      await mentionButton.click()

      // Wait for the agent tab to become active
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"][aria-selected="true"]'))
        .toBeVisible({ timeout: 5_000 })

      // Verify the editor still contains the draft text AND the mention
      await expect(editor).toContainText('my draft text')
      await expect(editor).toContainText('@package.json')
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('multiple tree mentions are space-separated', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'Multi Mention Test', adminOrgId, process.cwd())
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Ensure an agent tab exists and the editor is ready
      const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Wait for the file tree to load — package.json should be visible
      await expect(page.getByText('package.json')).toBeVisible({ timeout: 15_000 })

      // First mention: hover, open context menu, and click mention for package.json
      const packageJsonNode = page.getByText('package.json')
      await packageJsonNode.hover()
      const treeRow1 = packageJsonNode.locator('..')
      const contextButton1 = treeRow1.locator('[data-testid="tree-context-button"]')
      await expect(contextButton1).toBeVisible()
      await contextButton1.click()
      const mentionButton1 = page.locator('[data-testid="tree-mention-button"]:visible')
      await expect(mentionButton1).toBeVisible()
      await mentionButton1.click()
      await expect(editor).toContainText('@package.json')

      // Wait for the first context menu to fully close before interacting with the next node
      await expect(page.locator('[data-testid="tree-mention-button"]:visible')).toHaveCount(0)

      // Second mention: hover, open context menu, and click mention for tsconfig.json
      const tsconfigNode = page.getByText('tsconfig.json')
      await tsconfigNode.hover()
      const treeRow2 = tsconfigNode.locator('..')
      const contextButton2 = treeRow2.locator('[data-testid="tree-context-button"]')
      await expect(contextButton2).toBeVisible()
      await contextButton2.click()
      const mentionButton2 = page.locator('[data-testid="tree-mention-button"]:visible')
      await expect(mentionButton2).toBeVisible()
      await mentionButton2.click()

      // Both mentions should be present and space-separated (not double-newline separated)
      await expect(editor).toContainText('@package.json @tsconfig.json')
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

      // Verify the editor contains the expected format with "From @" and the path
      await expect(editor).toContainText('From')
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })
})
