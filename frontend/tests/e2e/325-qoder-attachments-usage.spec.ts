import { expectAttachmentOutcome, sendWithAttachment } from './helpers/attachments'
import { expectContextUsage } from './helpers/contextUsage'
import { sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, qoderTest } from './qoder-fixtures'

qoderTest.describe('Qoder CLI attachments and context usage', () => {
  // Qoder's plugin declares `attachments: { text, image, pdf, binary }`, so the
  // composer offers a pill for every kind and the send carries the file.
  qoderTest('the composer accepts a text attachment and sends it', async ({ qoderWorkspace, page, modelScript }) => {
    void qoderWorkspace
    await modelScript.queue({ text: 'Attachment received.' })
    await expectAttachmentOutcome(page, 'text', { supported: true, fileName: 'notes.txt' })
    await sendWithAttachment(page, 'Read this.')
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(page.locator('[data-testid="attachment-pill"]:visible')).toHaveCount(0)
  })

  qoderTest('the composer accepts an image attachment', async ({ qoderWorkspace, page, modelScript }) => {
    void qoderWorkspace
    await modelScript.queue({ text: 'Image received.' })
    await expectAttachmentOutcome(page, 'image', { supported: true, fileName: 'shot.png' })
    await sendWithAttachment(page, 'Describe this.')
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
  })

  // The usage block the mock reports is the only source of these counts. A
  // default of 1/1 would make every number equal; 12000/40 is the marker.
  qoderTest('the agent info grid follows the usage the model reports', async ({ qoderWorkspace, page, modelScript }) => {
    void qoderWorkspace
    const usage = { inputTokens: 12000, outputTokens: 40 }
    await modelScript.queue({ text: 'Usage recorded.', usage })
    await sendMessage(page, modelScript.prompt('Reply once.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectContextUsage(page, usage)
  })
})
