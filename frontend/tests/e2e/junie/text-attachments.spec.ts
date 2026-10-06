import { expectNativeAttachmentProof } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome, sendWithAttachment } from '../helpers/attachments'
import { junieAnswerToolCall } from '../helpers/providerToolCalls'
import { waitForAgentIdle } from '../helpers/ui'
import { junieTest } from '../junie-fixtures'

junieTest.describe('Junie attachments and context usage', () => {
  junieTest('delivers text attachment bytes to the model', async ({ authenticatedJunieWorkspace, page, modelScript }) => {
    void authenticatedJunieWorkspace
    await modelScript.queue({ toolCalls: [junieAnswerToolCall('junie-note-answer', 'The note is attached.')] })
    const sourcePath = await expectAttachmentOutcome(page, 'text', { supported: true, fileName: 'jnote.txt' })
    await sendWithAttachment(page, modelScript.prompt('Read the note.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expectNativeAttachmentProof(page, status, 'text', sourcePath)
  })
})
