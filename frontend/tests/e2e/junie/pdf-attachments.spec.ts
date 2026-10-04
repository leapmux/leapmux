import type { ModelScript } from '../helpers/modelScriptFixture'
import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'
import { junieAnswerToolCall } from '../helpers/providerToolCalls'
import { JUNIE_E2E_SKIP_REASON, junieTest } from '../junie-fixtures'

junieTest.describe('Junie attachments and context usage', () => {
  junieTest.skip(!!JUNIE_E2E_SKIP_REASON, JUNIE_E2E_SKIP_REASON || '')

  async function scriptJunieAttachmentHousekeeping(modelScript: ModelScript, title: string): Promise<void> {
    await modelScript.rule(
      { name: 'junie-attachment-capability-filter', when: { system: 'capability filter agent' }, respond: { text: '' } },
      { name: 'junie-attachment-task-name', when: { system: 'task description summarizer' }, respond: { text: title } },
    )
  }

  junieTest('refuses a PDF attachment before a model request', async ({ authenticatedJunieWorkspace, page, modelScript }) => {
    void authenticatedJunieWorkspace
    await scriptJunieAttachmentHousekeeping(modelScript, 'Clean PDF turn')
    const rejected = await expectAttachmentOutcome(page, 'pdf', { supported: false })
    await expectRefusedAttachmentsAbsent(page, modelScript, [rejected], {
      toolCalls: [junieAnswerToolCall('junie-clean-pdf', 'The clean prompt ended.')],
    })
  })
})
