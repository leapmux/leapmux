import { antigravityTest } from '../antigravity-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

antigravityTest('delivers the actual text attachment bytes through the native model request', async ({ native }) => {
  await exerciseAttachmentDelivery(native, 'text', 'antigravity-native.txt')
})
