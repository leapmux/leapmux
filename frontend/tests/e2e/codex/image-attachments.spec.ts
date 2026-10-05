import type { Page } from '@playwright/test'
import type { AttachmentKind } from '../helpers/attachments'
import type { ModelScript } from '../helpers/modelScriptFixture'
import { expect } from '@playwright/test'
import { codexTest } from '../codex-fixtures'
import { expectNativeAttachmentProof } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome, sendWithAttachment } from '../helpers/attachments'
import { assistantBubbles, expectUserMessage, waitForAgentIdle } from '../helpers/ui'

async function proveAttachment(page: Page, modelScript: ModelScript, kind: AttachmentKind, filename: string): Promise<void> {
  await modelScript.queue({ text: 'Attachment received.' })
  const sourcePath = await expectAttachmentOutcome(page, kind, { supported: true, fileName: filename })
  await sendWithAttachment(page, modelScript.prompt('Read the attached file.'))
  const status = await modelScript.waitForSteps()
  await expectNativeAttachmentProof(page, status, kind, sourcePath, 'openai-responses')
  await waitForAgentIdle(page)
  await expectUserMessage(page, filename)
  await expect(assistantBubbles(page).filter({ hasText: 'Attachment received.' }).first()).toBeVisible()
}

codexTest.describe('Codex attachment support', () => {
  codexTest('delivers an image attachment to the model', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace
    await proveAttachment(page, modelScript, 'image', 'codex-shot.png')
  })
})
