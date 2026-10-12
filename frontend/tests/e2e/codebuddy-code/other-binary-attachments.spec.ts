import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

codebuddyTest.describe('CodeBuddy Code binary attachments', () => {
  codebuddyTest('carries the bytes of a binary attachment to the model', async ({ native }) => {
    // CodeBuddy sends the file as a Chat Completions `file` part with the media type that the browser declares.
    // The browser takes the type of a `.bin` file from the platform, so the proof accepts any application type.
    await exerciseAttachmentDelivery(native, 'binary', 'codebuddy-blob.bin', { protocol: 'openai-chat-completions', binaryMediaType: /^application\// })
  })
})
