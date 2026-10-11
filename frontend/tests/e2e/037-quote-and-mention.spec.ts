import { expect } from '@playwright/test'
import { frontendRoot } from '~/test-support/sourceTree'
import { test } from './fixtures'
import { selectedAgentTab } from './helpers/nativeScenario'
import { QUICK_BROWN_FOX, sayExactly, sendScriptedTurn } from './helpers/scriptedTurn'
import { selectedText } from './helpers/selection'
import { waitTimeoutBeforeTestDeadline } from './helpers/testDeadline'
import { expectToastRecorded } from './helpers/toast'
import { agentTabs, ASSISTANT_BUBBLE_SELECTOR, clickTreeContextItem, composerEditor, firstAssistantMessageRow, treeRow } from './helpers/ui'

test.describe('Quote and Mention', () => {
  // The mention tests reach files of the frontend directory through the file tree.
  test.use({ agentWorkingDir: frontendRoot })

  test('reply button on assistant message inserts quoted text into editor', async ({ page, authenticatedWorkspace, modelScript }) => {
    const editor = composerEditor(page)

    // Send a message and wait for the assistant to reply
    await sendScriptedTurn(page, modelScript, sayExactly('Hello world'))

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

  test('cursor lands outside blockquote after quoting', async ({ page, authenticatedWorkspace, modelScript }) => {
    const editor = composerEditor(page)

    // Send a message and wait for the assistant to reply
    await sendScriptedTurn(page, modelScript, sayExactly('Hello world'))

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

  test('text selection copy button copies to clipboard', async ({ page, context, authenticatedWorkspace, modelScript }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])

    // Send a message and wait for the assistant to reply
    await sendScriptedTurn(page, modelScript, sayExactly(QUICK_BROWN_FOX))

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

  // A non-secure origin -- plain http:// on a LAN, which is how the app is read
  // on a phone -- exposes no `navigator.clipboard` at all. The harness serves on
  // localhost, which is always secure, so the property is taken away here
  // instead. `~/lib/clipboard` then falls back to `execCommand`, which is the
  // only path that copies anything there.
  test('text selection copy button copies with no Clipboard API', async ({ page, context, authenticatedWorkspace, modelScript }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])

    await sendScriptedTurn(page, modelScript, sayExactly(QUICK_BROWN_FOX))

    const assistantBubble = firstAssistantMessageRow(page).locator(ASSISTANT_BUBBLE_SELECTOR)
    await expect(assistantBubble).toBeVisible()
    const messageContent = assistantBubble.locator('[data-testid="message-content"]')

    // Park a known value on the clipboard FIRST. Without it a fallback that
    // copied nothing would still pass, because the previous test left the same
    // message text there.
    await page.evaluate(() => navigator.clipboard.writeText('nothing was copied'))
    // An OWN property shadowing the prototype getter, so deleting it below hands
    // the real API back for the read.
    await page.evaluate(() => {
      Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined })
    })

    await messageContent.click({ clickCount: 3 })
    const copyButton = page.locator('[data-testid="copy-selection-button"]')
    await expect(copyButton).toBeVisible()
    await copyButton.click()

    // The popover closes only on a write that landed, so its absence is the
    // app agreeing that something was copied.
    await expect(copyButton).toBeHidden()

    await page.evaluate(() => Reflect.deleteProperty(navigator, 'clipboard'))
    const clipboardText = await page.evaluate(() => navigator.clipboard.readText())
    expect(clipboardText).toContain('quick brown fox')
  })

  // Both paths gone. Clearing the highlight and closing the popover is what the
  // app uses to say "copied", so a failed write must do neither -- and must say
  // why, because a Copy button that silently does nothing reads as a dead button.
  test('text selection copy button keeps the selection and says why when nothing can copy', async ({ page, authenticatedWorkspace, modelScript }) => {
    await sendScriptedTurn(page, modelScript, sayExactly(QUICK_BROWN_FOX))

    const assistantBubble = firstAssistantMessageRow(page).locator(ASSISTANT_BUBBLE_SELECTOR)
    await expect(assistantBubble).toBeVisible()
    const messageContent = assistantBubble.locator('[data-testid="message-content"]')

    await page.evaluate(() => {
      Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined })
      document.execCommand = () => false
    })

    await messageContent.click({ clickCount: 3 })
    const copyButton = page.locator('[data-testid="copy-selection-button"]')
    await expect(copyButton).toBeVisible()
    await copyButton.click()

    await expectToastRecorded(page, 'Could not copy')
    // The popover stays up, so the button is still there to try again...
    await expect(copyButton).toBeVisible()
    // ...and so is the text it acts on.
    const selected = await selectedText(page)
    expect(selected).toContain('quick brown fox')
  })

  test('text selection in chat message shows quote popover', async ({ page, authenticatedWorkspace, modelScript }) => {
    const editor = composerEditor(page)

    // Send a message and wait for the assistant to reply
    await sendScriptedTurn(page, modelScript, sayExactly(QUICK_BROWN_FOX))

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

  test('AtSign mention button in DirectoryTree context menu', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    // Ensure an agent tab exists and the editor is ready
    const editor = composerEditor(page)
    await expect(editor).toBeVisible()

    // Wait for the file tree to load — package.json should be visible
    const row = treeRow(page, 'package.json')
    await expect(row).toBeVisible()

    // Open the menu and click "Mention in chat" as one retried unit: the
    // sidebar element is rebuilt when the active tab context changes, which
    // unmounts an already-open menu.
    await clickTreeContextItem(row, 'tree-mention-button')

    // Verify the editor contains @package.json (the path is relative to cwd)
    await expect(editor).toContainText('@package.json')
  })

  test('AtSign mention button in file view toolbar', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    // Ensure an agent tab exists and click it to populate MRU
    const agentTab = agentTabs(page)
    await expect(agentTab).toBeVisible()
    await agentTab.click()

    const editor = composerEditor(page)
    await expect(editor).toBeVisible()

    // Wait for the file tree to load
    await expect(treeRow(page, 'package.json')).toBeVisible()

    // Click on package.json to open it as a file tab
    await treeRow(page, 'package.json').click()

    // Wait for the file tab to appear and become active
    const fileTab = page.locator('[data-testid="tab"][data-tab-type="file"]')
    await expect(fileTab).toBeVisible()

    // Wait for the file content to load: its numbered lines render with it.
    await expect(page.locator('[data-line-num]:visible').first()).toBeVisible()

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
    await expect(selectedAgentTab(page)).toBeVisible()

    // Verify the editor contains @package.json
    await expect(editor).toContainText('@package.json')
  })

  test('file view mention preserves existing editor draft', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    // Ensure an agent tab exists and click it to populate MRU
    const agentTab = agentTabs(page)
    await expect(agentTab).toBeVisible()
    await agentTab.click()

    const editor = composerEditor(page)
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
    await expect(page.locator('[data-line-num]:visible').first()).toBeVisible()

    // Mention lives in the file viewer's actions dropdown — see the note on
    // the previous test for why this is not the toolbar button it once was.
    await page.locator('[data-testid="file-actions-trigger"]').click()
    const mentionButton = page.locator('[data-testid="file-actions-mention-button"]')
    await expect(mentionButton).toBeVisible()
    await mentionButton.click()

    // Wait for the agent tab to become active
    await expect(selectedAgentTab(page)).toBeVisible()

    // Verify the editor still contains the draft text AND the mention
    await expect(editor).toContainText('my draft text')
    await expect(editor).toContainText('@package.json')
  })

  test('multiple tree mentions are space-separated', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    // Ensure an agent tab exists and the editor is ready
    const editor = composerEditor(page)
    await expect(editor).toBeVisible()

    // Wait for the file tree to load — package.json should be visible
    const row1 = treeRow(page, 'package.json')
    await expect(row1).toBeVisible()

    // First mention: open the context menu and click mention for package.json
    await clickTreeContextItem(row1, 'tree-mention-button')
    await expect(editor).toContainText('@package.json')

    // Wait for the first context menu to fully close before interacting with the next node
    await expect(page.locator('[data-testid="tree-mention-button"]:visible')).toHaveCount(0)

    // Second mention: open the context menu and click mention for tsconfig.json
    await clickTreeContextItem(treeRow(page, 'tsconfig.json'), 'tree-mention-button')

    // Both mentions should be present and space-separated (not double-newline separated)
    await expect(editor).toContainText('@package.json @tsconfig.json')
  })

  test('text selection quote in file view inserts with file path and line numbers', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    // Ensure an agent tab exists and click it to populate MRU
    const agentTab = agentTabs(page)
    await expect(agentTab).toBeVisible()
    await agentTab.click()

    const editor = composerEditor(page)
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
    const lineElements = page.locator('[data-line-num]:visible')
    await expect(lineElements.first()).toBeVisible()

    // Triple-click a line to select text, retried as ONE unit with the popover
    // it is supposed to raise. The viewer re-renders as syntax highlighting
    // and the diff toolbar settle, and a click that lands mid-render selects
    // nothing -- leaving the quote button to time out with no clue that the
    // selection never happened.
    // nth(2) avoids the floating DiffModeToolbar at the top.
    //
    // Each attempt reads the button at once and clicks only while it is
    // hidden. The popover shows a frame after the release, so the attempt that
    // clicks fails its read, and the next attempt finds the button and does not
    // click again: a click on a shown popover would clear the selection that
    // raised it. The loop ends before the test deadline with its own message.
    const quoteButton = page.locator('[data-testid="quote-selection-button"]')
    await expect(async () => {
      if (!await quoteButton.isVisible())
        await lineElements.nth(2).click({ clickCount: 3 })
      expect(await quoteButton.isVisible(), 'the triple click raises the quote button').toBe(true)
    }).toPass({ intervals: [250, 500], timeout: waitTimeoutBeforeTestDeadline() })

    // Click the quote button
    await quoteButton.click()

    // Wait for the agent tab to become active (tab switch + component mount)
    await expect(selectedAgentTab(page)).toBeVisible()

    // Verify the editor contains the expected format with "From @" and the path
    await expect(editor).toContainText('From')
  })
})
