import { Buffer } from 'node:buffer'
import { writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { expect, test } from './fixtures'

/** Create a minimal 1x1 PNG file in a temp directory and return its path. */
function createTestPng(name = 'test.png'): string {
  // 1x1 red pixel PNG (67 bytes)
  const png = Buffer.from(
    'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwADhQGAWjR9awAAAABJRU5ErkJggg==',
    'base64',
  )
  const path = join(tmpdir(), `leapmux-e2e-${Date.now()}-${name}`)
  writeFileSync(path, png)
  return path
}

/** Create a minimal binary file (unsupported for the default provider) for rejection testing. */
function createTestBinary(name = 'test.bin'): string {
  const path = join(tmpdir(), `leapmux-e2e-${Date.now()}-${name}`)
  writeFileSync(path, Buffer.from([0x00, 0xFF, 0x01, 0xFE]))
  return path
}

test.describe('Attachment Support', () => {
  test('upload button opens file dialog and attachment appears in strip', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // The upload button should be visible in the toolbar.
    const uploadBtn = page.locator('[data-testid="toolbar-upload"]')
    await expect(uploadBtn).toBeVisible()

    // Upload a file via the hidden input.
    const fileInput = page.locator('[data-testid="file-input"]')
    await fileInput.setInputFiles(createTestPng('screenshot.png'))

    // Attachment strip should appear with one pill.
    const strip = page.locator('[data-testid="attachment-strip"]')
    await expect(strip).toBeVisible()
    const pill = page.locator('[data-testid="attachment-pill"]')
    await expect(pill).toHaveCount(1)
    await expect(pill).toContainText('screenshot.png')
  })

  test('remove attachment via X button', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Upload a file.
    const fileInput = page.locator('[data-testid="file-input"]')
    await fileInput.setInputFiles(createTestPng())

    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(1)

    // Click the remove button.
    await page.locator('[data-testid="attachment-remove"]').click()
    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(0)
    // Strip should be hidden when empty.
    await expect(page.locator('[data-testid="attachment-strip"]')).not.toBeVisible()
  })

  test('attachments survive tab switch', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Upload a file.
    const fileInput = page.locator('[data-testid="file-input"]')
    await fileInput.setInputFiles(createTestPng('persist.png'))
    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(1)

    // Open a new agent tab.
    await page.locator('[data-testid^="new-agent-button"]').first().click()
    await page.waitForTimeout(1000)

    // No attachments on the new tab.
    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(0)

    // Switch back to first tab.
    const agentTabs = page.locator('[data-testid="tab"][data-tab-type="agent"]')
    await agentTabs.first().click()
    await page.waitForTimeout(500)

    // Attachment should still be there.
    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(1)
    await expect(page.locator('[data-testid="attachment-pill"]')).toContainText('persist.png')
  })

  test('attachments cleared after send', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Upload a file.
    const fileInput = page.locator('[data-testid="file-input"]')
    await fileInput.setInputFiles(createTestPng())
    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(1)

    // Type some text and send.
    await editor.click()
    await page.keyboard.type('look at this')
    await page.keyboard.press('Meta+Enter')

    // Attachments should be cleared.
    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(0)
  })

  test('paste image adds attachment', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Simulate pasting an image file via clipboard event.
    await page.evaluate(() => {
      const blob = new Blob([new Uint8Array([0x89, 0x50, 0x4E, 0x47])], { type: 'image/png' })
      const file = new File([blob], 'pasted.png', { type: 'image/png' })
      const dt = new DataTransfer()
      dt.items.add(file)
      const event = new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true })
      document.querySelector('[data-testid="chat-editor"]')!.dispatchEvent(event)
    })

    // An attachment pill should appear.
    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(1)
  })

  test('paste image adds attachment when clipboardData.files is empty (Linux/WebKitGTK shape)', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // WebKitGTK exposes pasted clipboard images via items only; synthesize
    // that shape since Chromium's DataTransfer doesn't reproduce it.
    await page.evaluate(() => {
      const blob = new Blob([new Uint8Array([0x89, 0x50, 0x4E, 0x47])], { type: 'image/png' })
      const file = new File([blob], '', { type: 'image/png' })
      const fakeItem = {
        kind: 'file',
        type: 'image/png',
        getAsFile: () => file,
      } as unknown as DataTransferItem
      const fakeClipboardData = {
        files: [] as unknown as FileList,
        items: [fakeItem] as unknown as DataTransferItemList,
      }
      const event = new Event('paste', { bubbles: true, cancelable: true })
      Object.defineProperty(event, 'clipboardData', { value: fakeClipboardData })
      document.querySelector('[data-testid="chat-editor"]')!.dispatchEvent(event)
    })

    // An attachment pill should appear.
    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(1)
  })

  test('paste image adds attachment when delivered as <img src="blob:..."> in text/html (Linux/WebKitGTK shape)', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // WebKitGTK on Tauri/Linux delivers pasted clipboard images as
    // text/html containing <img src="blob:...">, with .files empty and
    // no file-kind entries in .items. The blob URL is minted against the
    // page origin, so the page can fetch it. Reproduce that shape here.
    await page.evaluate(() => {
      // Minimal valid 1×1 PNG (67 bytes).
      const pngBase64 = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwADhQGAWjR9awAAAABJRU5ErkJggg=='
      const bin = atob(pngBase64)
      const bytes = new Uint8Array(bin.length)
      for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i)
      const blob = new Blob([bytes], { type: 'image/png' })
      const blobUrl = URL.createObjectURL(blob)

      const html = `<img src="${blobUrl}"/>`
      const fakeClipboardData = {
        files: [] as unknown as FileList,
        items: [] as unknown as DataTransferItemList,
        types: ['text/html'],
        getData: (type: string) => (type === 'text/html' ? html : ''),
      }
      const event = new Event('paste', { bubbles: true, cancelable: true })
      Object.defineProperty(event, 'clipboardData', { value: fakeClipboardData })
      document.querySelector('[data-testid="chat-editor"]')!.dispatchEvent(event)
    })

    // The fetch + onPaste path is async — give it a moment to resolve.
    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(1)

    // The editor must NOT contain the blob URL — that would mean ProseMirror
    // processed the HTML before our handler intercepted it.
    const editorHtml = await editor.innerHTML()
    expect(editorHtml).not.toContain('blob:')
  })

  test('unsupported file type rejected with toast', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Upload a binary file (unsupported type for the default provider).
    const fileInput = page.locator('[data-testid="file-input"]')
    await fileInput.setInputFiles(createTestBinary())

    // No attachment pill should appear.
    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(0)

    // A toast should have been shown in the DOM (output element with .toast-message).
    const toast = page.locator('output .toast-message')
    await expect(toast).toContainText('binary')
  })

  test('attachment-only message (no text) can be sent', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Upload a file without typing any text.
    const fileInput = page.locator('[data-testid="file-input"]')
    await fileInput.setInputFiles(createTestPng('solo.png'))
    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(1)

    // The send button should be enabled even without text.
    const sendBtn = page.locator('[data-testid="send-button"]')
    await expect(sendBtn).toBeEnabled()

    // Click send.
    await sendBtn.click()

    // Attachment should be cleared.
    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(0)
  })

  test('drag and drop adds attachment', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Simulate drag and drop via the file input (Playwright doesn't natively
    // support drag-and-drop of files from the OS, so we use the file input).
    const fileInput = page.locator('[data-testid="file-input"]')
    await fileInput.setInputFiles(createTestPng('dropped.png'))

    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(1)
    await expect(page.locator('[data-testid="attachment-pill"]')).toContainText('dropped.png')
  })

  test('chat history shows attachment list in user message', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Upload a file and send with text.
    const fileInput = page.locator('[data-testid="file-input"]')
    await fileInput.setInputFiles(createTestPng('history.png'))
    await editor.click()
    await page.keyboard.type('analyze this image')
    await page.keyboard.press('Meta+Enter')

    // The user message bubble should contain the attachment filename.
    // The optimistic message is rendered immediately.
    await page.waitForTimeout(500)
    const userBubbles = page.locator('[class*="userMessage"]')
    const lastBubble = userBubbles.last()
    await expect(lastBubble).toContainText('history.png')
    await expect(lastBubble).toContainText('analyze this image')
  })
})
