import { expect } from '@playwright/test'
import { expectNativeAttachmentProof } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome, sendWithAttachment } from '../helpers/attachments'
import { assistantBubbles, expectUserMessage, waitForAgentIdle } from '../helpers/ui'
import { opencodeTest } from '../opencode-fixtures'

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
