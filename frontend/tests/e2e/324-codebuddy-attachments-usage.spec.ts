import { CODEBUDDY_E2E_SKIP_REASON, codebuddyTest, expect } from './codebuddy-fixtures'
import { expectNativeAttachmentProof } from './helpers/attachmentModelProbe'
import { expectAttachmentOutcome, sendWithAttachment } from './helpers/attachments'
import { expectContextUsage } from './helpers/contextUsage'
import { sendMessage, waitForAgentIdle } from './helpers/ui'

codebuddyTest.skip(!!CODEBUDDY_E2E_SKIP_REASON, CODEBUDDY_E2E_SKIP_REASON || '')

codebuddyTest.describe('CodeBuddy Code attachments and context usage', () => {
  // CodeBuddy's plugin declares `attachments: { text, image, pdf, binary }`, so
  // the composer offers a pill for every kind and the send carries the file.
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

  codebuddyTest('the model receives an image attachment', async ({ codebuddyWorkspace, page, modelScript }) => {
    void codebuddyWorkspace
    await modelScript.queue({ text: 'Image received.' })
    const sourcePath = await expectAttachmentOutcome(page, 'image', { supported: true, fileName: 'shot.png', readyGroup: 'permissionMode' })
    await sendWithAttachment(page, modelScript.prompt('Describe this.'))
    const status = await modelScript.waitForSteps()
    await expectNativeAttachmentProof(page, status, 'image', sourcePath, 'openai-chat-completions')
    await waitForAgentIdle(page)
  })

  codebuddyTest('the model receives a PDF attachment', async ({ codebuddyWorkspace, page, modelScript }) => {
    void codebuddyWorkspace
    await modelScript.queue({ text: 'Document received.' })
    const sourcePath = await expectAttachmentOutcome(page, 'pdf', { supported: true, fileName: 'codebuddy-doc.pdf', readyGroup: 'permissionMode' })
    await sendWithAttachment(page, modelScript.prompt('Read this document.'))
    const status = await modelScript.waitForSteps()
    await expectNativeAttachmentProof(page, status, 'pdf', sourcePath, 'openai-chat-completions')
    await waitForAgentIdle(page)
  })

  // The usage block the mock reports is the only source of these counts. A
  // default of 1/1 would make every number equal; 12000/40 is the marker.
  codebuddyTest('the agent info grid follows the usage the model reports', async ({ codebuddyWorkspace, page, modelScript }) => {
    void codebuddyWorkspace
    const usage = { inputTokens: 12000, outputTokens: 40 }
    await modelScript.queue({ text: 'Usage recorded.', usage })
    await sendMessage(page, modelScript.prompt('Reply once.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectContextUsage(page, usage)
  })
})
