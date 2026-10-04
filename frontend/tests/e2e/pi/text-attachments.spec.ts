import type { Page } from '@playwright/test'
import type { AttachmentKind } from '../helpers/attachments'
import type { ModelScript } from '../helpers/modelScriptFixture'
import { expect } from '@playwright/test'
import { expectNativeAttachmentProof } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome, sendWithAttachment } from '../helpers/attachments'
import { assistantBubbles, expectUserMessage, waitForAgentIdle } from '../helpers/ui'
import { PI_E2E_SKIP_REASON, piTest } from '../pi-fixtures'

piTest.skip(!!PI_E2E_SKIP_REASON, PI_E2E_SKIP_REASON || '')

async function proveAttachment(page: Page, modelScript: ModelScript, kind: AttachmentKind, filename: string): Promise<void> {
  await modelScript.queue({ text: 'Attachment received.' })
  const sourcePath = await expectAttachmentOutcome(page, kind, { supported: true, fileName: filename })
  await sendWithAttachment(page, modelScript.prompt('Read the attached file.'))
  const status = await modelScript.waitForSteps()
  await expectNativeAttachmentProof(page, status, kind, sourcePath)
  await waitForAgentIdle(page)
  await expectUserMessage(page, filename)
  await expect(assistantBubbles(page).filter({ hasText: 'Attachment received.' }).first()).toBeVisible()
}

piTest('delivers a text attachment to the model', async ({ authenticatedPiWorkspace, page, modelScript }) => {
  void authenticatedPiWorkspace
  await proveAttachment(page, modelScript, 'text', 'pi-notes.txt')
})
