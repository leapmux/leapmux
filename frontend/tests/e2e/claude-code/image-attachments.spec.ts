import { expect } from '@playwright/test'
import { test } from '../fixtures'
import { expectNativeAttachmentProof } from '../helpers/attachmentModelProbe'
import { writeAttachmentFixture } from '../helpers/attachments'
import { waitForAgentIdle } from '../helpers/ui'

test.describe('Attachment Support', () => {
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
