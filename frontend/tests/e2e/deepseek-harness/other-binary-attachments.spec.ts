import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseDeepseekHarnessFileAttachment } from './attachmentScenarios'
import { nativeContext } from './scenarios'

deepseekHarnessTest('reads all binary attachment bytes including zero and non-UTF8 bytes', async ({ deepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: deepseekHarnessWorkspace.workspaceId })
  await exerciseDeepseekHarnessFileAttachment(context, 'binary', 'native-bytes.bin')
})
