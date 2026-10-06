import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseDeepseekHarnessFileAttachment } from './attachmentScenarios'
import { nativeContext } from './scenarios'

deepseekHarnessTest('reads the complete uploaded text through its native saved file and keeps the attachment after reload', async ({ askingDeepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingDeepseekHarnessWorkspace.workspaceId })
  await exerciseDeepseekHarnessFileAttachment(context, 'text', 'native-notes.txt')
})
