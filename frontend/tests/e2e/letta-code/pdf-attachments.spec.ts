import { expect } from '@playwright/test'
import { expectAttachmentOutcome } from '../helpers/attachments'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

lettaTest('refuses a pdf attachment before it reaches the actual native model', async ({ authenticatedLettaWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedLettaWorkspace.workspaceId })
  const before = (await modelScript.status()).requests.length
  await expectAttachmentOutcome(page, 'pdf', { supported: false, readyGroup: 'permissionMode' })
  expect((await modelScript.status()).requests.length).toBe(before)
  const request = await sendNativeAnswer(context, 'Reply after refusing the unsupported attachment.', 'The supported native turn completed.')
  expect(JSON.stringify(request.body)).not.toContain('.pdf')
  await page.reload()
  await expectAttachmentOutcome(page, 'pdf', { supported: false, readyGroup: 'permissionMode' })
})
