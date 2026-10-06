import { codebuddyTest } from '../codebuddy-fixtures'
import { expectNativeAttachmentProof } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome, sendWithAttachment } from '../helpers/attachments'
import { waitForAgentIdle } from '../helpers/ui'

codebuddyTest.describe('CodeBuddy Code attachments and context usage', () => {
  codebuddyTest('the model receives an image attachment', async ({ authenticatedCodebuddyWorkspace, page, modelScript }) => {
    void authenticatedCodebuddyWorkspace
    await modelScript.queue({ text: 'Image received.' })
    const sourcePath = await expectAttachmentOutcome(page, 'image', { supported: true, fileName: 'shot.png', readyGroup: 'permissionMode' })
    await sendWithAttachment(page, modelScript.prompt('Describe this.'))
    const status = await modelScript.waitForSteps()
    await expectNativeAttachmentProof(page, status, 'image', sourcePath, 'openai-chat-completions')
    await waitForAgentIdle(page)
  })
})
