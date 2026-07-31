import path from 'node:path'
import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { ASSISTANT_BUBBLE_SELECTOR, clickTreeContextItem, firstAssistantMessageRow, loginViaToken, openWorkspace, sendMessage, treeRow, waitForAgentIdle } from './helpers/ui'

const frontendDir = path.resolve(import.meta.dirname, '../..')

test.describe('Quote and Mention', () => {
  test('reply button on assistant message inserts quoted text into editor', async ({ page, authenticatedWorkspace }) => {
    // Wait for the editor to be ready
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Send a message and wait for the assistant to reply
    await sendMessage(page, 'Say exactly: Hello world')
    await waitForAgentIdle(page)

    // Find the assistant MESSAGE row -- not merely the first agent bubble, which
    // can be a notice or turn-end divider with no reply button on it.
    const messageRow = firstAssistantMessageRow(page)
    await expect(messageRow).toBeVisible()

    // Hover the row to reveal the reply button (it's hidden by default via opacity: 0)
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
    const messageRow = firstAssistantMessageRow(page)
    await expect(messageRow).toBeVisible()
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
    const assistantBubble = firstAssistantMessageRow(page).locator(ASSISTANT_BUBBLE_SELECTOR)
    await expect(assistantBubble).toBeVisible()

    const messageContent = assistantBubble.locator('[data-testid="message-content"]')

    // Triple-click to select all text in the message (triggers mouseup → popover)
    await messageContent.click({ clickCount: 3 })

    // The copy button should appear
    const copyButton = page.locator('[data-testid="copy-selection-button"]')
    await expect(copyButton).toBeVisible()

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
    const assistantBubble = firstAssistantMessageRow(page).locator(ASSISTANT_BUBBLE_SELECTOR)
    await expect(assistantBubble).toBeVisible()

    const messageContent = assistantBubble.locator('[data-testid="message-content"]')

    // Triple-click to select all text in the message (triggers mouseup → popover)
    await messageContent.click({ clickCount: 3 })

    // The quote popover should appear
    const quoteButton = page.locator('[data-testid="quote-selection-button"]')
    await expect(quoteButton).toBeVisible()

    // Click the quote button
    await quoteButton.click()

    // Verify the editor now contains a blockquote (Milkdown renders > text as <blockquote>)
    await expect(editor.locator('blockquote')).toBeVisible()
  })

  test('AtSign mention button in DirectoryTree context menu', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Tree Mention Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      // Ensure an agent tab exists and the editor is ready
      const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Wait for the file tree to load — package.json should be visible
      const row = treeRow(page, 'package.json')
      await expect(row).toBeVisible()

      // Open the menu and click "Mention in chat" as one retried unit: the
      // sidebar element is rebuilt when the active tab context changes, which
      // unmounts an already-open menu.
      await clickTreeContextItem(page, row, 'tree-mention-button')

      // Verify the editor contains @package.json (the path is relative to cwd)
      await expect(editor).toContainText('@package.json')
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('AtSign mention button in file view toolbar', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'File Mention Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      // Ensure an agent tab exists and click it to populate MRU
      const agentTab = page.locator('[data-testid="tab"][data-tab-type="agent"]')
      await expect(agentTab).toBeVisible()
      await agentTab.click()

      const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Wait for the file tree to load
      await expect(treeRow(page, 'package.json')).toBeVisible()

      // Click on package.json to open it as a file tab
      await treeRow(page, 'package.json').click()

      // Wait for the file tab to appear and become active
      const fileTab = page.locator('[data-testid="tab"][data-tab-type="file"]')
      await expect(fileTab).toBeVisible()

      // Wait for file content to load
      await page.waitForTimeout(1000)

      // The mention action lives in the file viewer's actions dropdown, so
      // open that first. (This spec was still looking for a floating-toolbar
      // `file-mention-button`, a testid that no longer exists anywhere in src;
      // 014-workspace-archive has the current shape.)
      await page.locator('[data-testid="file-actions-trigger"]').click()
      const mentionButton = page.locator('[data-testid="file-actions-mention-button"]')
      await expect(mentionButton).toBeVisible()

      // Click the mention button
      await mentionButton.click()

      // Wait for the agent tab to become active (tab switch + component mount)
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"][aria-selected="true"]'))
        .toBeVisible()

      // Verify the editor contains @package.json
      await expect(editor).toContainText('@package.json')
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('file view mention preserves existing editor draft', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Mention Preserve Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

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
      await expect(treeRow(page, 'package.json')).toBeVisible()

      // Click on package.json to open it as a file tab
      await treeRow(page, 'package.json').click()

      const fileTab = page.locator('[data-testid="tab"][data-tab-type="file"]')
      await expect(fileTab).toBeVisible()
      await page.waitForTimeout(1000)

      // Mention lives in the file viewer's actions dropdown — see the note on
      // the previous test for why this is not the toolbar button it once was.
      await page.locator('[data-testid="file-actions-trigger"]').click()
      const mentionButton = page.locator('[data-testid="file-actions-mention-button"]')
      await expect(mentionButton).toBeVisible()
      await mentionButton.click()

      // Wait for the agent tab to become active
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"][aria-selected="true"]'))
        .toBeVisible()

      // Verify the editor still contains the draft text AND the mention
      await expect(editor).toContainText('my draft text')
      await expect(editor).toContainText('@package.json')
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('multiple tree mentions are space-separated', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Multi Mention Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      // Ensure an agent tab exists and the editor is ready
      const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Wait for the file tree to load — package.json should be visible
      const row1 = treeRow(page, 'package.json')
      await expect(row1).toBeVisible()

      // First mention: open the context menu and click mention for package.json
      await clickTreeContextItem(page, row1, 'tree-mention-button')
      await expect(editor).toContainText('@package.json')

      // Wait for the first context menu to fully close before interacting with the next node
      await expect(page.locator('[data-testid="tree-mention-button"]:visible')).toHaveCount(0)

      // Second mention: open the context menu and click mention for tsconfig.json
      await clickTreeContextItem(page, treeRow(page, 'tsconfig.json'), 'tree-mention-button')

      // Both mentions should be present and space-separated (not double-newline separated)
      await expect(editor).toContainText('@package.json @tsconfig.json')
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('text selection quote in file view inserts with file path and line numbers', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'File Quote Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      // Ensure an agent tab exists and click it to populate MRU
      const agentTab = page.locator('[data-testid="tab"][data-tab-type="agent"]')
      await expect(agentTab).toBeVisible()
      await agentTab.click()

      const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Wait for the file tree to load
      const row = treeRow(page, 'package.json')
      await expect(row).toBeVisible()

      // Click on package.json to open it as a file tab
      await row.click()

      // Wait for the file tab and content to load
      const fileTab = page.locator('[data-testid="tab"][data-tab-type="file"]')
      await expect(fileTab).toBeVisible()

      // Wait for line-numbered content to appear (data-line-num attributes)
      const lineElements = page.locator('[data-line-num]')
      await expect(lineElements.first()).toBeVisible()

      // Triple-click a line to select text, retried as ONE unit with the popover
      // it is supposed to raise. The viewer re-renders as syntax highlighting
      // and the diff toolbar settle, and a click that lands mid-render selects
      // nothing -- leaving the quote button to time out 120s later with no clue
      // that the selection never happened.
      // nth(2) avoids the floating DiffModeToolbar at the top.
      const quoteButton = page.locator('[data-testid="quote-selection-button"]')
      await expect(async () => {
        await lineElements.nth(2).click({ clickCount: 3 })
        // Bounded rather than inheriting the global 120s: the enclosing
        // toPass IS the retry loop, and an attempt that spent two minutes
        // inside a single assertion would leave no budget for the re-click
        // this block exists for.
        await expect(quoteButton).toBeVisible({ timeout: 5000 })
      }).toPass({ timeout: 45_000, intervals: [250, 500] })

      // Click the quote button
      await quoteButton.click()

      // Wait for the agent tab to become active (tab switch + component mount)
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"][aria-selected="true"]'))
        .toBeVisible()

      // Verify the editor contains the expected format with "From @" and the path
      await expect(editor).toContainText('From')
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })
})
