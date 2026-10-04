import type { ModelScript } from '../helpers/modelScriptFixture'
import { expectNativeAttachmentProof } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome, sendWithAttachment } from '../helpers/attachments'
import { junieAnswerToolCall } from '../helpers/providerToolCalls'
import { waitForAgentIdle } from '../helpers/ui'
import { JUNIE_E2E_SKIP_REASON, junieTest } from '../junie-fixtures'

junieTest.describe('Junie attachments and context usage', () => {
  junieTest.skip(!!JUNIE_E2E_SKIP_REASON, JUNIE_E2E_SKIP_REASON || '')

  async function scriptJunieAttachmentHousekeeping(modelScript: ModelScript, title: string): Promise<void> {
    await modelScript.rule(
      { name: 'junie-attachment-capability-filter', when: { system: 'capability filter agent' }, respond: { text: '' } },
      { name: 'junie-attachment-task-name', when: { system: 'task description summarizer' }, respond: { text: title } },
    )
  }

  junieTest('delivers text attachment bytes to the model', async ({ authenticatedJunieWorkspace, page, modelScript }) => {
    void authenticatedJunieWorkspace
    await scriptJunieAttachmentHousekeeping(modelScript, 'Read note')
    await modelScript.queue({ toolCalls: [junieAnswerToolCall('junie-note-answer', 'The note is attached.')] })
    const sourcePath = await expectAttachmentOutcome(page, 'text', { supported: true, fileName: 'jnote.txt' })
    await sendWithAttachment(page, modelScript.prompt('Read the note.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expectNativeAttachmentProof(page, status, 'text', sourcePath)
  })
})
