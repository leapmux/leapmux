import { expect } from '@playwright/test'
import { test } from '../fixtures'
import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { writeAttachmentFixture } from '../helpers/attachments'

test.describe('Attachment Support', () => {
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
})
