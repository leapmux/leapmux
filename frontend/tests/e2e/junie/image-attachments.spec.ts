import { expectNativeAttachmentProof } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome, sendWithAttachment } from '../helpers/attachments'
import { junieAnswerToolCall } from '../helpers/providerToolCalls'
import { waitForAgentIdle } from '../helpers/ui'
import { junieTest } from '../junie-fixtures'

junieTest.describe('Junie attachments and context usage', () => {
  junieTest('delivers image attachment bytes to the model', async ({ authenticatedJunieWorkspace, page, modelScript }) => {
    void authenticatedJunieWorkspace
    await modelScript.queue({ toolCalls: [junieAnswerToolCall('junie-image-answer', 'The image is attached.')] })
    const sourcePath = await expectAttachmentOutcome(page, 'image', { supported: true, fileName: 'jshot.png' })
    await sendWithAttachment(page, modelScript.prompt('Inspect the image.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    // Junie decodes the PNG with Java ImageIO and writes a new PNG (ImageContentHelper.adjustImageData), so the pixels
    // stay and the bytes change. The current user turn must carry an image/png part with the four quadrant colors.
    await expectNativeAttachmentProof(page, status, 'image', sourcePath, 'openai-chat-completions', { transcodedImageType: 'image/png' })
  })
})
