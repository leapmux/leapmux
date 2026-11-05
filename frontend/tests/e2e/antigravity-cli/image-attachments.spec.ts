import { antigravityTest } from '../antigravity-fixtures'
import { exerciseImageAttachmentDelivery } from '../helpers/attachmentModelProbe'

antigravityTest('delivers the actual image attachment bytes through the native model request', async ({ native }) => {
  await exerciseImageAttachmentDelivery(native, 'antigravity-native.png', { protocol: 'google-generative-language' })
})
