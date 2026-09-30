import { CODEWHALE_E2E_SKIP_REASON, codewhaleTest, expect } from './codewhale-fixtures'
import { exerciseAttachmentDelivery } from './helpers/attachmentModelProbe'
import { CODEWHALE_VISION_MODEL_ID } from './helpers/mockAgentEnvironment'
import { chooseSettingsOption, expectSettingsOptionChosen, waitForSettingsHydrated, waitForSettingsIdle } from './helpers/ui'

codewhaleTest.skip(!!CODEWHALE_E2E_SKIP_REASON, CODEWHALE_E2E_SKIP_REASON || '')

codewhaleTest.describe('Codewhale attachments', () => {
  codewhaleTest('delivers the contents of a text attachment to the model', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'text', 'codewhale-notes.txt')
  })

  codewhaleTest('delivers an image attachment to the model', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, `model-${CODEWHALE_VISION_MODEL_ID}`)
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, `model-${CODEWHALE_VISION_MODEL_ID}`)
    await exerciseAttachmentDelivery(page, modelScript, 'image', 'codewhale-shot.png')
    const request = (await modelScript.status()).requests.find(record => record.stepIndex === 0)
    if (!request?.body || typeof request.body !== 'object' || !('model' in request.body))
      throw new Error('the Codewhale image request must state its selected model')
    expect(request.body.model).toBe(CODEWHALE_VISION_MODEL_ID)
  })
})
