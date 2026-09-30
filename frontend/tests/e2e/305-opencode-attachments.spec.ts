import { expectNativeAttachmentProof, expectRefusedAttachmentsAbsent } from './helpers/attachmentModelProbe'
import { expectAttachmentOutcome, sendWithAttachment } from './helpers/attachments'
import { assistantBubbles, expectUserMessage, waitForAgentIdle } from './helpers/ui'
import { expect, OPENCODE_E2E_SKIP_REASON, opencodeTest } from './opencode-fixtures'

opencodeTest.skip(!!OPENCODE_E2E_SKIP_REASON, OPENCODE_E2E_SKIP_REASON || '')

/**
 * 305 -- OpenCode attachments.
 *
 * Text, image and PDF complete a turn. The composer refuses binary input,
 * which the OpenCode model path cannot send.
 */
opencodeTest.describe('OpenCode attachments', () => {
  // The file name states a word the prompt never gives, so the name in the user
  // message can only come from the attachment.
  opencodeTest('accepts a text attachment and carries it through the turn', async ({ authenticatedOpencodeWorkspace, page, modelScript }) => {
    void authenticatedOpencodeWorkspace
    await modelScript.queue({ text: 'The note is attached.' })
    const sourcePath = await expectAttachmentOutcome(page, 'text', { supported: true, fileName: 'opencode-notes.txt' })
    await sendWithAttachment(page, modelScript.prompt('Read the attached note.'))
    const status = await modelScript.waitForSteps()
    await expectNativeAttachmentProof(page, status, 'text', sourcePath, 'openai-chat-completions')
    await waitForAgentIdle(page)

    await expectUserMessage(page, 'opencode-notes.txt')
    await expect(assistantBubbles(page).filter({ hasText: 'The note is attached.' }).first()).toBeVisible()
  })

  opencodeTest('delivers an image attachment through the model turn', async ({ authenticatedOpencodeWorkspace, page, modelScript }) => {
    void authenticatedOpencodeWorkspace
    await modelScript.queue({ text: 'The image is attached.' })
    const sourcePath = await expectAttachmentOutcome(page, 'image', { supported: true, fileName: 'opencode-shot.png' })
    await sendWithAttachment(page, modelScript.prompt('Describe the attached image.'))
    const status = await modelScript.waitForSteps()
    await expectNativeAttachmentProof(page, status, 'image', sourcePath, 'openai-chat-completions')
    await waitForAgentIdle(page)

    await expectUserMessage(page, 'opencode-shot.png')
    await expect(assistantBubbles(page).filter({ hasText: 'The image is attached.' }).first()).toBeVisible()
  })

  opencodeTest('accepts a PDF attachment and carries it through the turn', async ({ authenticatedOpencodeWorkspace, page, modelScript }) => {
    void authenticatedOpencodeWorkspace
    await modelScript.queue({ text: 'The PDF is attached.' })
    const sourcePath = await expectAttachmentOutcome(page, 'pdf', { supported: true, fileName: 'opencode-doc.pdf' })
    await sendWithAttachment(page, modelScript.prompt('Read the attached PDF.'))
    const status = await modelScript.waitForSteps()
    await expectNativeAttachmentProof(page, status, 'pdf', sourcePath, 'openai-chat-completions')
    await waitForAgentIdle(page)

    await expectUserMessage(page, 'opencode-doc.pdf')
    await expect(assistantBubbles(page).filter({ hasText: 'The PDF is attached.' }).first()).toBeVisible()
  })

  opencodeTest('refuses a binary attachment before it enters the queue', async ({ authenticatedOpencodeWorkspace, page, modelScript }) => {
    void authenticatedOpencodeWorkspace
    const rejected = await expectAttachmentOutcome(page, 'binary', { supported: false, fileName: 'opencode-blob.bin' })
    await expectRefusedAttachmentsAbsent(page, modelScript, [rejected])
  })
})
