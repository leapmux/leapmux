import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { codewhaleTest } from '../codewhale-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { CODEWHALE_VISION_MODEL_ID } from '../helpers/mockAgentEnvironment'
import { chooseSettingsOption, expectSettingsOptionChosen, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

codewhaleTest.describe('Codewhale attachments', () => {
  codewhaleTest('delivers an image attachment to the model', async ({ native, page }) => {
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, `model-${CODEWHALE_VISION_MODEL_ID}`)
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, `model-${CODEWHALE_VISION_MODEL_ID}`)
    const request = await exerciseAttachmentDelivery(native, 'image', 'codewhale-shot.png')
    if (!isObject(request.body))
      throw new Error('the Codewhale image request must state its selected model')
    expect(request.body.model, 'the Codewhale image request states the selected vision model').toBe(CODEWHALE_VISION_MODEL_ID)
  })
})
