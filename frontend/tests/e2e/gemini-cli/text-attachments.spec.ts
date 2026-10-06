import { geminiTest } from '../gemini-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

geminiTest('delivers the actual text attachment bytes through the native model request', async ({ native }) => {
  await exerciseAttachmentDelivery(native, 'text', 'gemini-native.txt')
})
