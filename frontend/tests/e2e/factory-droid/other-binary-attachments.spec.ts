import { DROID_E2E_SKIP_REASON, DROID_TITLE_RULE, droidTest } from '../droid-fixtures'
import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'

droidTest.describe('Factory Droid attachments', () => {
  droidTest.skip(!!DROID_E2E_SKIP_REASON, DROID_E2E_SKIP_REASON || '')

  droidTest('refuses another binary attachment before a model request', async ({ authenticatedDroidWorkspace, page, modelScript }) => {
    void authenticatedDroidWorkspace
    await modelScript.rule(DROID_TITLE_RULE)
    const rejected = await expectAttachmentOutcome(page, 'binary', { supported: false })
    await expectRefusedAttachmentsAbsent(page, modelScript, [rejected])
  })
})
