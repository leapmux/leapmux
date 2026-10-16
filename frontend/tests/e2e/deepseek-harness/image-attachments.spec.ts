import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseImageAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { expectUserMessage } from '../helpers/ui'

deepseekHarnessTest('sends the actual image pixels through native image admission and keeps its attachment', async ({ native, page }) => {
  await exerciseImageAttachmentDelivery(native, 'native-colors.png', { protocol: 'anthropic-messages' })
  await page.reload()
  await expectUserMessage(page, 'native-colors.png')
})
