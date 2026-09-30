import { exerciseAttachmentDelivery } from './helpers/attachmentModelProbe'
import { OH_MY_PI_E2E_SKIP_REASON, ohMyPiTest } from './ohmypi-fixtures'

ohMyPiTest.skip(!!OH_MY_PI_E2E_SKIP_REASON, OH_MY_PI_E2E_SKIP_REASON || '')

ohMyPiTest.describe('Oh My Pi attachments', () => {
  ohMyPiTest('delivers the contents of a text attachment to the model', async ({ authenticatedOhMyPiWorkspace, page, modelScript }) => {
    void authenticatedOhMyPiWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'text', 'omp-notes.txt')
  })

  ohMyPiTest('delivers an image attachment to the model', async ({ authenticatedOhMyPiWorkspace, page, modelScript }) => {
    void authenticatedOhMyPiWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'image', 'omp-shot.png')
  })
})
