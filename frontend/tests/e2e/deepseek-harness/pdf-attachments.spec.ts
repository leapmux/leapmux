import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseDeepseekHarnessFileAttachment } from './attachmentScenarios'
import { nativeContext } from './scenarios'

deepseekHarnessTest('reads every uploaded PDF byte through its native saved file', async ({ authenticatedDeepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDeepseekHarnessWorkspace.workspaceId })
  await exerciseDeepseekHarnessFileAttachment(context, 'pdf', 'native-document.pdf')
})
