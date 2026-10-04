import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseDeepseekHarnessFileAttachment } from './attachmentScenarios'
import { nativeContext } from './scenarios'

deepseekHarnessTest('reads every uploaded PDF byte through its native saved file', async ({ deepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: deepseekHarnessWorkspace.workspaceId })
  await exerciseDeepseekHarnessFileAttachment(context, 'pdf', 'native-document.pdf')
})
