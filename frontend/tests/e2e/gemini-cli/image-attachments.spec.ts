import { geminiTest } from '../gemini-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

geminiTest('delivers the actual image attachment bytes through the native model request', async ({ native }) => {
  await exerciseAttachmentDelivery(native, 'image', 'gemini-native.png', { protocol: 'google-generative-language' })
})
