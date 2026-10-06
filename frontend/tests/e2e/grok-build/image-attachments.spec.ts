import { join } from 'node:path'
import { grokTest } from '../grok-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { createTestDirectory } from '../helpers/runDirectory'
import { writeToolImage } from '../helpers/toolImages'

grokTest.describe('Grok Build attachments', () => {
  grokTest('accepts an image attachment and carries it through the turn', async ({ authenticatedGrokWorkspace, page, modelScript }) => {
    void authenticatedGrokWorkspace
    const directory = createTestDirectory('grok-attachment-image-')
    const fileName = writeToolImage(directory, 'grok-attachment')
    const imagePath = join(directory, fileName)
    await exerciseAttachmentDelivery(page, modelScript, 'image', fileName, { fixturePath: imagePath })
  })
})
