import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest.describe('Oh My Pi attachments', () => {
  ohMyPiTest('delivers the contents of a text attachment to the model', async ({ authenticatedOhMyPiWorkspace, page, modelScript }) => {
    void authenticatedOhMyPiWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'text', 'omp-notes.txt')
  })
})
