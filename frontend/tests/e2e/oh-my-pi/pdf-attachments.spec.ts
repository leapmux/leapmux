import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest('refuses the attachment kind and excludes its actual bytes from the next native request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'pdf', fileName: 'oh-my-pi-refused.pdf' })
})
