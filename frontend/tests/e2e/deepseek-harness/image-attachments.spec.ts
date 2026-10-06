import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { expectNativeAttachmentProof } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome, sendWithAttachment } from '../helpers/attachments'
import { assistantBubbles, expectUserMessage, waitForAgentIdle } from '../helpers/ui'

deepseekHarnessTest('sends the actual image pixels through native image admission and keeps its attachment', async ({ authenticatedDeepseekHarnessWorkspace, page, modelScript }) => {
  void authenticatedDeepseekHarnessWorkspace
  const sourcePath = await expectAttachmentOutcome(page, 'image', { supported: true, fileName: 'native-colors.png' })
  await modelScript.queue({ text: 'The native image attachment reached the model.' })
  await sendWithAttachment(page, modelScript.prompt('Inspect the actual image pixels.'))
  const status = await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  await expectNativeAttachmentProof(page, status, 'image', sourcePath, 'anthropic-messages')
  await expectUserMessage(page, 'native-colors.png')
  await expect(assistantBubbles(page).filter({ hasText: 'The native image attachment reached the model.' }).first()).toBeVisible()
  await page.reload()
  await expectUserMessage(page, 'native-colors.png')
})
