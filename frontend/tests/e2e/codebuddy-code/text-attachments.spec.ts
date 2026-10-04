import { CODEBUDDY_E2E_SKIP_REASON, codebuddyTest, expect } from '../codebuddy-fixtures'
import { expectNativeAttachmentProof } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome, sendWithAttachment } from '../helpers/attachments'
import { waitForAgentIdle } from '../helpers/ui'

codebuddyTest.describe('CodeBuddy Code attachments and context usage', () => {
  codebuddyTest.skip(!!CODEBUDDY_E2E_SKIP_REASON, CODEBUDDY_E2E_SKIP_REASON || '')

  codebuddyTest('the model receives a text attachment', async ({ codebuddyWorkspace, page, modelScript }) => {
    void codebuddyWorkspace
    await modelScript.queue({ text: 'Attachment received.' })
    const sourcePath = await expectAttachmentOutcome(page, 'text', { supported: true, fileName: 'notes.txt', readyGroup: 'permissionMode' })
    await sendWithAttachment(page, modelScript.prompt('Read this.'))
    const status = await modelScript.waitForSteps()
    await expectNativeAttachmentProof(page, status, 'text', sourcePath, 'openai-chat-completions')
    await waitForAgentIdle(page)
    await expect(page.locator('[data-testid="attachment-pill"]:visible')).toHaveCount(0)
  })
})
