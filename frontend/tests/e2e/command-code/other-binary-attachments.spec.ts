import { commandCodeTest } from '../command-code-fixtures'
import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'

commandCodeTest('refuses the unsupported native binary attachment and keeps it out of the next request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, 'binary')
})
