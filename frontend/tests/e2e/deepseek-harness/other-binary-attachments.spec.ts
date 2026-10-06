import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseDeepseekHarnessFileAttachment } from './attachmentScenarios'
import { nativeContext } from './scenarios'

deepseekHarnessTest('reads all binary attachment bytes including zero and non-UTF8 bytes', async ({ authenticatedDeepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDeepseekHarnessWorkspace.workspaceId })
  await exerciseDeepseekHarnessFileAttachment(context, 'binary', 'native-bytes.bin')
})
