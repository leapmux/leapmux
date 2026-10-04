import { expect } from '@playwright/test'
import { expectNativeAttachmentProof } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome, sendWithAttachment } from '../helpers/attachments'
import { assistantBubbles, expectUserMessage, waitForAgentIdle } from '../helpers/ui'
import { OPENCODE_E2E_SKIP_REASON, opencodeTest } from '../opencode-fixtures'

opencodeTest.skip(!!OPENCODE_E2E_SKIP_REASON, OPENCODE_E2E_SKIP_REASON || '')

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
