import { codebuddyTest } from '../codebuddy-fixtures'
import { expectNativeAttachmentProof } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome, sendWithAttachment } from '../helpers/attachments'
import { waitForAgentIdle } from '../helpers/ui'

codebuddyTest.describe('CodeBuddy Code attachments and context usage', () => {
  codebuddyTest('the model receives a PDF attachment', async ({ authenticatedCodebuddyWorkspace, page, modelScript }) => {
    void authenticatedCodebuddyWorkspace
    await modelScript.queue({ text: 'Document received.' })
    const sourcePath = await expectAttachmentOutcome(page, 'pdf', { supported: true, fileName: 'codebuddy-doc.pdf', readyGroup: 'permissionMode' })
    await sendWithAttachment(page, modelScript.prompt('Read this document.'))
    const status = await modelScript.waitForSteps()
    // CodeBuddy turns the stream-json `document` block into a Chat Completions
    // `file` part with a PDF data URI.
    await expectNativeAttachmentProof(page, status, 'pdf', sourcePath, 'openai-chat-completions')
    await waitForAgentIdle(page)
  })
})
