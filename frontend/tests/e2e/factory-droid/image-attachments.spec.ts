import { DROID_E2E_SKIP_REASON, DROID_TITLE_RULE, droidTest } from '../droid-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

droidTest.describe('Factory Droid attachments', () => {
  droidTest.skip(!!DROID_E2E_SKIP_REASON, DROID_E2E_SKIP_REASON || '')

  droidTest('delivers image attachment bytes to the model', async ({ authenticatedDroidWorkspace, page, modelScript }) => {
    void authenticatedDroidWorkspace
    await modelScript.rule(DROID_TITLE_RULE)
    // Droid decodes the PNG and encodes it again with pngjs before the request,
    // so the part holds a new RGBA PNG with the same four colors.
    await exerciseAttachmentDelivery(page, modelScript, 'image', 'droid-shot.png', { protocol: 'openai-chat-completions', transcodedImageType: 'image/png' })
  })
})
