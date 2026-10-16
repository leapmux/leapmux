import { geminiTest } from '../gemini-fixtures'
import { exerciseImageAttachmentDelivery } from '../helpers/attachmentModelProbe'

geminiTest('delivers the actual image attachment bytes through the native model request', async ({ native }) => {
  await exerciseImageAttachmentDelivery(native, 'gemini-native.png', { protocol: 'google-generative-language' })
})
