import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest.describe('Oh My Pi attachments', () => {
  ohMyPiTest('delivers the contents of a text attachment to the model', async ({ native }) => {
    await exerciseAttachmentDelivery(native, 'text', 'omp-notes.txt')
  })
})
