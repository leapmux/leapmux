import { expect } from '@playwright/test'
import { test } from '../fixtures'
import { expectNativeAttachmentProof } from '../helpers/attachmentModelProbe'
import { writeAttachmentFixture } from '../helpers/attachments'
import { waitForAgentIdle } from '../helpers/ui'

test.describe('Attachment Support', () => {
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
})
