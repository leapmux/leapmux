import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'
import { kiroTest } from '../kiro-fixtures'

kiroTest.describe('attachments for Kiro', () => {
  kiroTest('refuses a binary file', async ({ native }) => {
    await exerciseAttachmentRefusal(native, 'binary')
  })
})

// Track this LeapMux limitation: https://github.com/leapmux/leapmux/issues/511.
