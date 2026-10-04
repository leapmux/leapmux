import { commandCodeTest } from '../command-code-fixtures'
import { expectNativeAttachmentProof } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome, sendWithAttachment } from '../helpers/attachments'
import { waitForAgentIdle } from '../helpers/ui'

commandCodeTest('sends actual image attachment bytes through the native input path', async ({ commandCodeWorkspace, page, modelScript }) => {
  void commandCodeWorkspace
  await modelScript.queue({ text: 'The native attachment turn completed.' })
  const path = await expectAttachmentOutcome(page, 'image', { supported: true, readyGroup: 'permissionMode' })
  await sendWithAttachment(page, modelScript.prompt('Read the supplied native attachment.'))
  const status = await modelScript.waitForSteps()
  await expectNativeAttachmentProof(page, status, 'image', path, 'openai-chat-completions')
  await waitForAgentIdle(page)
})
