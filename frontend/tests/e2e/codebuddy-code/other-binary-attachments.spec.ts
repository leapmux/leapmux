import { codebuddyTest, expect } from '../codebuddy-fixtures'
import { expectAttachmentOutcome, sendWithAttachment } from '../helpers/attachments'
import { assistantBubbles, expectUserMessage, waitForAgentIdle } from '../helpers/ui'

codebuddyTest.describe('CodeBuddy Code binary attachments', () => {
  codebuddyTest('carries the bytes of a binary attachment to the model', async ({ authenticatedCodebuddyWorkspace, page, modelScript }) => {
    void authenticatedCodebuddyWorkspace
    await modelScript.queue({ text: 'The binary file reached the model.' })
    await expectAttachmentOutcome(page, 'binary', { supported: true, fileName: 'codebuddy-blob.bin', readyGroup: 'permissionMode' })
    await sendWithAttachment(page, modelScript.prompt('Inspect the attached binary file.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    const request = status.requests.find(record => record.stepIndex === 0)
    expect(request?.protocol).toBe('openai-chat-completions')
    const body = JSON.stringify(request?.body) ?? ''
    expect(/data:application\/[^;,]+;base64,AP8B\/g==/.test(body)).toBe(true)
    await expectUserMessage(page, 'codebuddy-blob.bin')
    await expect(assistantBubbles(page).filter({ hasText: 'The binary file reached the model.' })).toBeVisible()
  })
})
