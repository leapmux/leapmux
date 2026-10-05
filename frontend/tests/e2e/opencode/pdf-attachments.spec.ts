import { expect } from '@playwright/test'
import { expectNativeAttachmentProof } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome, sendWithAttachment } from '../helpers/attachments'
import { assistantBubbles, expectUserMessage, waitForAgentIdle } from '../helpers/ui'
import { OPENCODE_E2E_SKIP_REASON, opencodeTest } from '../opencode-fixtures'

opencodeTest.skip(!!OPENCODE_E2E_SKIP_REASON, OPENCODE_E2E_SKIP_REASON || '')

opencodeTest('accepts a PDF attachment and carries it through the turn', async ({ authenticatedOpencodeWorkspace, page, modelScript }) => {
  void authenticatedOpencodeWorkspace
  await modelScript.queue({ text: 'The PDF is attached.' })
  const sourcePath = await expectAttachmentOutcome(page, 'pdf', { supported: true, fileName: 'opencode-doc.pdf' })
  await sendWithAttachment(page, modelScript.prompt('Read the attached PDF.'))
  const status = await modelScript.waitForSteps()
  // OpenCode keeps the ACP blob as a data URL file part, and its Chat Completions
  // serializer sends it unchanged as a `file` part with a PDF data URI.
  await expectNativeAttachmentProof(page, status, 'pdf', sourcePath, 'openai-chat-completions')
  await waitForAgentIdle(page)

  await expectUserMessage(page, 'opencode-doc.pdf')
  await expect(assistantBubbles(page).filter({ hasText: 'The PDF is attached.' }).first()).toBeVisible()
})
