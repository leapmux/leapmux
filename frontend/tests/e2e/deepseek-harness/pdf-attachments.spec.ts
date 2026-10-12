import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseDeepseekHarnessFileAttachment } from './attachmentScenarios'

deepseekHarnessTest('reads every uploaded PDF byte through its native saved file', async ({ native }) => {
  await exerciseDeepseekHarnessFileAttachment(native, 'pdf', 'native-document.pdf')
})
