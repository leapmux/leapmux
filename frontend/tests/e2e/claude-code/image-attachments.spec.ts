import { expect } from '@playwright/test'
import { claudeTest } from '../claude-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { attachFile, attachmentPills, writeAttachmentFixture } from '../helpers/attachments'
import { composerEditor } from '../helpers/ui'
import { nativeContext } from './scenarios'

claudeTest.describe('Attachment Support', () => {
  claudeTest('attachment-only message (no text) can be sent', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    await expect(composerEditor(page)).toBeVisible()

    // Upload a file without typing any text.
    await attachFile(page, writeAttachmentFixture('image', 'solo.png'))
    await expect(attachmentPills(page)).toHaveCount(1)

    // The send button should be enabled even without text.
    const sendBtn = page.locator('[data-testid="send-button"]:visible')
    await expect(sendBtn).toBeEnabled()

    // Click send.
    await sendBtn.click()

    // Attachment should be cleared.
    await expect(attachmentPills(page)).toHaveCount(0)
  })

  claudeTest('the model receives an image and the user row keeps its filename', async ({ page, modelScript, leapmuxServer, authenticatedWorkspace }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedWorkspace.workspaceId })
    await exerciseAttachmentDelivery(context, 'image', 'history.png', { protocol: 'anthropic-messages' })
  })
})
