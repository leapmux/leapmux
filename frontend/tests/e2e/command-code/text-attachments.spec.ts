import { commandCodeTest } from '../command-code-fixtures'
import { expectNativeAttachmentProof } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome, sendWithAttachment } from '../helpers/attachments'
import { waitForAgentIdle } from '../helpers/ui'

commandCodeTest('sends actual text attachment bytes through the native input path', async ({ authenticatedCommandCodeWorkspace, page, modelScript }) => {
  void authenticatedCommandCodeWorkspace
  await modelScript.queue({ text: 'The native attachment turn completed.' })
  const path = await expectAttachmentOutcome(page, 'text', { supported: true, readyGroup: 'permissionMode' })
  await sendWithAttachment(page, modelScript.prompt('Read the supplied native attachment.'))
  const status = await modelScript.waitForSteps()
  await expectNativeAttachmentProof(page, status, 'text', path, 'openai-chat-completions')
  await waitForAgentIdle(page)
})
