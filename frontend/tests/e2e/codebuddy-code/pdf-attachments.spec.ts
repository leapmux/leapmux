import { CODEBUDDY_E2E_SKIP_REASON, codebuddyTest } from '../codebuddy-fixtures'
import { expectNativeAttachmentProof } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome, sendWithAttachment } from '../helpers/attachments'
import { waitForAgentIdle } from '../helpers/ui'

codebuddyTest.describe('CodeBuddy Code attachments and context usage', () => {
  codebuddyTest.skip(!!CODEBUDDY_E2E_SKIP_REASON, CODEBUDDY_E2E_SKIP_REASON || '')

  codebuddyTest('the model receives a PDF attachment', async ({ codebuddyWorkspace, page, modelScript }) => {
    void codebuddyWorkspace
    await modelScript.queue({ text: 'Document received.' })
    const sourcePath = await expectAttachmentOutcome(page, 'pdf', { supported: true, fileName: 'codebuddy-doc.pdf', readyGroup: 'permissionMode' })
    await sendWithAttachment(page, modelScript.prompt('Read this document.'))
    const status = await modelScript.waitForSteps()
    await expectNativeAttachmentProof(page, status, 'pdf', sourcePath, 'openai-chat-completions')
    await waitForAgentIdle(page)
  })
})
