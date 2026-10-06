import type { Page } from '@playwright/test'
import type { AttachmentKind } from '../helpers/attachments'
import type { ModelScript } from '../helpers/modelScriptFixture'
import { expect } from '@playwright/test'
import { expectNativeAttachmentProof } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome, sendWithAttachment } from '../helpers/attachments'
import { assistantBubbles, expectUserMessage, waitForAgentIdle } from '../helpers/ui'
import { kiloTest } from '../kilo-fixtures'

async function proveAttachment(page: Page, modelScript: ModelScript, kind: AttachmentKind, filename: string): Promise<void> {
  await modelScript.queue({ text: 'Attachment received.' })
  const sourcePath = await expectAttachmentOutcome(page, kind, { supported: true, fileName: filename })
  await sendWithAttachment(page, modelScript.prompt('Read the attached file.'))
  const status = await modelScript.waitForSteps()
  // Kilo keeps the ACP blob as a data URL file part, and its Chat Completions
  // serializer sends it unchanged as a `file` part with a PDF data URI.
  await expectNativeAttachmentProof(page, status, kind, sourcePath, 'openai-chat-completions')
  await waitForAgentIdle(page)
  await expectUserMessage(page, filename)
  await expect(assistantBubbles(page).filter({ hasText: 'Attachment received.' }).first()).toBeVisible()
}

kiloTest('delivers a pdf attachment to the model', async ({ authenticatedKiloWorkspace, page, modelScript }) => {
  void authenticatedKiloWorkspace
  await proveAttachment(page, modelScript, 'pdf', 'kilo-doc.pdf')
})
