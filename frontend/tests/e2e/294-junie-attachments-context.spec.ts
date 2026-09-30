import type { ModelScript } from './helpers/modelScriptFixture'
import { expectNativeAttachmentProof, expectRefusedAttachmentsAbsent } from './helpers/attachmentModelProbe'
import { expectAttachmentOutcome, sendWithAttachment } from './helpers/attachments'
import { expectContextUsage } from './helpers/contextUsage'
import { junieAnswerToolCall } from './helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from './helpers/ui'
import { JUNIE_E2E_SKIP_REASON, junieTest } from './junie-fixtures'

junieTest.skip(!!JUNIE_E2E_SKIP_REASON, JUNIE_E2E_SKIP_REASON || '')

async function scriptJunieAttachmentHousekeeping(modelScript: ModelScript, title: string): Promise<void> {
  await modelScript.rule(
    { name: 'junie-attachment-capability-filter', when: { system: 'capability filter agent' }, respond: { text: '' } },
    { name: 'junie-attachment-task-name', when: { system: 'task description summarizer' }, respond: { text: title } },
  )
}

junieTest.describe('Junie attachments and context usage', () => {
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

  junieTest('delivers image attachment bytes to the model', async ({ authenticatedJunieWorkspace, page, modelScript }) => {
    void authenticatedJunieWorkspace
    await scriptJunieAttachmentHousekeeping(modelScript, 'Inspect image')
    await modelScript.queue({ toolCalls: [junieAnswerToolCall('junie-image-answer', 'The image is attached.')] })
    const sourcePath = await expectAttachmentOutcome(page, 'image', { supported: true, fileName: 'jshot.png' })
    await sendWithAttachment(page, modelScript.prompt('Inspect the image.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expectNativeAttachmentProof(page, status, 'image', sourcePath)
  })

  junieTest('refuses a PDF attachment before a model request', async ({ authenticatedJunieWorkspace, page, modelScript }) => {
    void authenticatedJunieWorkspace
    await scriptJunieAttachmentHousekeeping(modelScript, 'Clean PDF turn')
    const rejected = await expectAttachmentOutcome(page, 'pdf', { supported: false })
    await expectRefusedAttachmentsAbsent(page, modelScript, [rejected], {
      toolCalls: [junieAnswerToolCall('junie-clean-pdf', 'The clean prompt ended.')],
    })
  })

  junieTest('refuses another binary attachment before a model request', async ({ authenticatedJunieWorkspace, page, modelScript }) => {
    void authenticatedJunieWorkspace
    await scriptJunieAttachmentHousekeeping(modelScript, 'Clean binary turn')
    const rejected = await expectAttachmentOutcome(page, 'binary', { supported: false })
    await expectRefusedAttachmentsAbsent(page, modelScript, [rejected], {
      toolCalls: [junieAnswerToolCall('junie-clean-binary', 'The clean prompt ended.')],
    })
  })

  // The usage block the mock reports is the only source of these counts. A
  // default of 1/1 would make every number equal; 12000/40 is the marker.
  junieTest('the agent info grid follows the usage the model reports', async ({ authenticatedJunieWorkspace, page, modelScript }) => {
    void authenticatedJunieWorkspace
    await scriptJunieAttachmentHousekeeping(modelScript, 'Usage task')
    const usage = { inputTokens: 12000, outputTokens: 40 }
    await modelScript.queue({
      toolCalls: [junieAnswerToolCall('junie-usage-answer', 'Usage recorded.')],
      usage,
    })
    await sendMessage(page, modelScript.prompt('Reply once.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectContextUsage(page, usage)
  })
})
