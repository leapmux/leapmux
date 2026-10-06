import { expect } from '@playwright/test'
import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'
import { lettaTest } from '../letta-fixtures'

lettaTest('refuses a binary attachment before it reaches the actual native model', async ({ native, page, modelScript }) => {
  const before = (await modelScript.status()).requests.length
  const rejected = await expectAttachmentOutcome(page, 'binary', { supported: false })
  expect((await modelScript.status()).requests.length).toBe(before)
  const clean = await expectRefusedAttachmentsAbsent(native, [rejected])
  // The shared check reads the file name in the user content only. No part of the clean request names a binary file.
  expect(JSON.stringify(clean.body), 'the clean Letta request names no binary file').not.toContain('.bin')
  await page.reload()
  await expectAttachmentOutcome(page, 'binary', { supported: false })
})
