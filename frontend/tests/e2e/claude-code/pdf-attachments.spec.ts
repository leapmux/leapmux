import { expect } from '@playwright/test'
import { test } from '../fixtures'
import { expectNativeAttachmentProof } from '../helpers/attachmentModelProbe'
import { writeAttachmentFixture } from '../helpers/attachments'
import { waitForAgentIdle } from '../helpers/ui'

test.describe('Attachment Support', () => {
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
})
