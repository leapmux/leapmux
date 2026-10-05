import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { LETTA_E2E_SKIP_REASON, LETTA_TITLE_RULE, lettaTest } from '../letta-fixtures'

lettaTest.describe('Letta Code attachments and context usage', () => {
  lettaTest.skip(!!LETTA_E2E_SKIP_REASON, LETTA_E2E_SKIP_REASON || '')

  lettaTest('delivers image attachment bytes to the model', async ({ authenticatedVisionLettaWorkspace, page, modelScript }) => {
    void authenticatedVisionLettaWorkspace
    await modelScript.rule(LETTA_TITLE_RULE)
    await exerciseAttachmentDelivery(page, modelScript, 'image', 'letta-shot.png', { protocol: 'openai-responses' })
  })
})
