import { geminiTest } from '../gemini-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

geminiTest('delivers the actual pdf attachment bytes through the native model request', async ({ native }) => {
  await exerciseAttachmentDelivery(native, 'pdf', 'gemini-native.pdf')
})
