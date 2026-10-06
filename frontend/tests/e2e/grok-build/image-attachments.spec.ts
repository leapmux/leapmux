import { join } from 'node:path'
import { grokTest } from '../grok-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { createTestDirectory } from '../helpers/runDirectory'
import { writeToolImage } from '../helpers/toolImages'

grokTest.describe('Grok Build attachments', () => {
  grokTest('accepts an image attachment and carries it through the turn', async ({ native }) => {
    const directory = createTestDirectory('grok-attachment-image-')
    const fileName = writeToolImage(directory, 'grok-attachment')
    await exerciseAttachmentDelivery(native, 'image', fileName, { fixturePath: join(directory, fileName) })
  })
})
