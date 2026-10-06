import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseDeepseekHarnessFileAttachment } from './attachmentScenarios'

deepseekHarnessTest('reads all binary attachment bytes including zero and non-UTF8 bytes', async ({ native }) => {
  await exerciseDeepseekHarnessFileAttachment(native, 'binary', 'native-bytes.bin')
})
