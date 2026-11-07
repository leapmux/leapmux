import { antigravityTest } from '../antigravity-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

antigravityTest('delivers the actual pdf attachment bytes through the native model request', async ({ native }) => {
  await exerciseAttachmentDelivery(native, 'pdf', 'antigravity-native.pdf')
})
