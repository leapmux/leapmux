import { expect, test } from './fixtures'
import { expectNativeAttachmentProof, expectRefusedAttachmentsAbsent } from './helpers/attachmentModelProbe'
import { writeAttachmentFixture } from './helpers/attachments'
import { expectClipsToOneLine, waitForAgentIdle } from './helpers/ui'

test.describe('Attachment Support', () => {
  test('attach item opens file dialog and attachment appears in strip', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Attach moved from the deleted formatting toolbar into the `[+]` menu.
    await page.locator('[data-testid="composer-plus-trigger"]').click()
    const attach = page.locator('[data-testid="composer-attach-file"]')
    await expect(attach).toBeVisible()
    await expect(attach).toBeEnabled()
    await page.keyboard.press('Escape')

    // Upload a file via the hidden input.
    const fileInput = page.locator('[data-testid="file-input"]')
    await fileInput.setInputFiles(writeAttachmentFixture('image', 'screenshot.png'))

    // Attachment strip should appear with one pill.
    const strip = page.locator('[data-testid="attachment-strip"]')
    await expect(strip).toBeVisible()
    const pill = page.locator('[data-testid="attachment-pill"]')
    await expect(pill).toHaveCount(1)
    await expect(pill).toContainText('screenshot.png')

    // The file name clips to one line inside the pill's 200px cap. It declared
    // the ellipsis before but not the `min-width: 0` a flex item needs to shrink
    // past its own text; only a real browser resolves the composed rules.
    //
    // `span[class]` selects the LABEL. Tooltip wraps its child in a bare
    // `display: contents` span, which also holds the text and comes first, so a
    // plain `span` locator resolves to that wrapper instead.
    await expectClipsToOneLine(pill.locator('span[class]').filter({ hasText: 'screenshot.png' }))
  })

  test('remove attachment via X button', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Upload a file.
    const fileInput = page.locator('[data-testid="file-input"]')
    await fileInput.setInputFiles(writeAttachmentFixture('image'))

    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(1)

    // Click the remove button.
    await page.locator('[data-testid="attachment-remove"]').click()
    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(0)
    // Strip should be hidden when empty.
    await expect(page.locator('[data-testid="attachment-strip"]')).not.toBeVisible()
  })

  test('attachments survive tab switch', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Upload a file.
    const fileInput = page.locator('[data-testid="file-input"]')
    await fileInput.setInputFiles(writeAttachmentFixture('image', 'persist.png'))
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
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Upload a file.
    const fileInput = page.locator('[data-testid="file-input"]')
    await fileInput.setInputFiles(writeAttachmentFixture('image'))
    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(1)

    // Type some text and send.
    await editor.click()
    await page.keyboard.type('look at this')
    await page.keyboard.press('Meta+Enter')

    // Attachments should be cleared.
    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(0)
  })

  test('paste image adds attachment', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Simulate pasting an image file via clipboard event.
    await page.evaluate(() => {
      const blob = new Blob([new Uint8Array([0x89, 0x50, 0x4E, 0x47])], { type: 'image/png' })
      const file = new File([blob], 'pasted.png', { type: 'image/png' })
      const dt = new DataTransfer()
      dt.items.add(file)
      const event = new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true })
      document.querySelector('[data-testid="composer-editor"]')!.dispatchEvent(event)
    })

    // An attachment pill should appear.
    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(1)
  })

  test('paste image adds attachment when clipboardData.files is empty (Linux/WebKitGTK shape)', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // WebKitGTK exposes pasted images through items but leaves files empty.
    // Override Chromium's files getter to reproduce that clipboard shape.
    await page.evaluate(() => {
      const blob = new Blob([new Uint8Array([0x89, 0x50, 0x4E, 0x47])], { type: 'image/png' })
      const file = new File([blob], '', { type: 'image/png' })
      const clipboardData = new DataTransfer()
      clipboardData.items.add(file)
      Object.defineProperty(clipboardData, 'files', { value: new DataTransfer().files })
      const event = new ClipboardEvent('paste', { clipboardData, bubbles: true, cancelable: true })
      document.querySelector('[data-testid="composer-editor"]')!.dispatchEvent(event)
    })

    // An attachment pill should appear.
    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(1)
  })

  // The Linux/WebKitGTK image-paste path (entirely empty DataTransfer →
  // OS clipboard read via the Tauri clipboard-manager plugin) cannot be
  // exercised in headless Chromium: the bug is that WebKitGTK does not
  // populate DataTransfer at all, and Chromium does not reproduce that
  // shape. The conversion logic is covered by the platformBridge unit
  // test; manual paste in the desktop build is the only true end-to-end.

  test('unsupported file type stays out of the next model request', async ({ page, authenticatedWorkspace, modelScript }) => {
    void authenticatedWorkspace
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Upload a binary file (unsupported type for the default provider).
    const fileInput = page.locator('[data-testid="file-input"]')
    const rejected = writeAttachmentFixture('binary')
    await fileInput.setInputFiles(rejected)

    // No attachment pill should appear.
    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(0)

    // A toast should have been shown in the DOM (output element with .toast-message).
    const toast = page.locator('output .toast-message')
    await expect(toast).toContainText('binary')
    await expectRefusedAttachmentsAbsent(page, modelScript, [rejected])
  })

  // Claude Code is one of the providers the matrix marks for PDF attachments.
  // The accept attribute and the kind classifier both have unit coverage; this
  // is the composer path end to end.
  test('a PDF reaches the model', async ({ page, authenticatedWorkspace, modelScript }) => {
    void authenticatedWorkspace
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    const fileInput = page.locator('[data-testid="file-input"]')
    const sourcePath = writeAttachmentFixture('pdf', 'spec.pdf')
    await fileInput.setInputFiles(sourcePath)
    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(1)
    await expect(page.locator('[data-testid="attachment-pill"]').first()).toContainText('spec.pdf')

    await editor.click()
    await modelScript.queue({ text: 'The document arrived.' })
    await page.keyboard.type(modelScript.prompt('Read the attached PDF.'))
    await page.keyboard.press('Meta+Enter')
    await expect(editor).toHaveText('')
    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(0)
    const status = await modelScript.waitForSteps()
    await expectNativeAttachmentProof(page, status, 'pdf', sourcePath, 'anthropic-messages')
    await waitForAgentIdle(page)
  })

  test('a text file reaches the model', async ({ page, authenticatedWorkspace, modelScript }) => {
    void authenticatedWorkspace
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    const fileInput = page.locator('[data-testid="file-input"]')
    const sourcePath = writeAttachmentFixture('text', 'notes.txt')
    await fileInput.setInputFiles(sourcePath)
    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(1)
    await expect(page.locator('[data-testid="attachment-pill"]').first()).toContainText('notes.txt')

    await editor.click()
    await modelScript.queue({ text: 'The note arrived.' })
    await page.keyboard.type(modelScript.prompt('Summarize the attached notes.'))
    await page.keyboard.press('Meta+Enter')
    await expect(editor).toHaveText('')
    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(0)
    const status = await modelScript.waitForSteps()
    await expectNativeAttachmentProof(page, status, 'text', sourcePath, 'anthropic-messages')
    await waitForAgentIdle(page)
  })

  test('attachment-only message (no text) can be sent', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Upload a file without typing any text.
    const fileInput = page.locator('[data-testid="file-input"]')
    await fileInput.setInputFiles(writeAttachmentFixture('image', 'solo.png'))
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
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Simulate drag and drop via the file input (Playwright doesn't natively
    // support drag-and-drop of files from the OS, so we use the file input).
    const fileInput = page.locator('[data-testid="file-input"]')
    await fileInput.setInputFiles(writeAttachmentFixture('image', 'dropped.png'))

    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(1)
    await expect(page.locator('[data-testid="attachment-pill"]')).toContainText('dropped.png')
  })

  test('the model receives an image and the user row keeps its filename', async ({ page, authenticatedWorkspace, modelScript }) => {
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Upload a file and send with text.
    const fileInput = page.locator('[data-testid="file-input"]')
    const sourcePath = writeAttachmentFixture('image', 'history.png')
    await fileInput.setInputFiles(sourcePath)
    await editor.click()
    await modelScript.queue({ text: 'The image arrived.' })
    await page.keyboard.type(modelScript.prompt('analyze this image'))
    await page.keyboard.press('Meta+Enter')
    const status = await modelScript.waitForSteps()
    await expectNativeAttachmentProof(page, status, 'image', sourcePath, 'anthropic-messages')
    await waitForAgentIdle(page)

    // The accepted user message contains the attachment filename.
    const userBubbles = page.locator('[class*="userMessage"]')
    const lastBubble = userBubbles.last()
    await expect(lastBubble).toContainText('history.png')
    await expect(lastBubble).toContainText('analyze this image')
  })
})
