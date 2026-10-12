import { commandCodeTest } from '../command-code-fixtures'
import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'

commandCodeTest('refuses the unsupported native pdf attachment and keeps it out of the next request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, 'pdf')
})
