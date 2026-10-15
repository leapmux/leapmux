import { join } from 'node:path'
import { exerciseImageAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { createTestDirectory } from '../helpers/runDirectory'
import { writeToolImage } from '../helpers/toolImages'
import { kiroTest } from '../kiro-fixtures'

kiroTest.describe('attachments for Kiro', () => {
  kiroTest('accepts an image attachment and carries it through the turn', async ({ native }) => {
    const directory = createTestDirectory('kiro-attachment-image-')
    const fileName = writeToolImage(directory, 'kiro-attachment')
    await exerciseImageAttachmentDelivery(native, fileName, { fixturePath: join(directory, fileName), protocol: 'aws-event-stream' })
  })
})
