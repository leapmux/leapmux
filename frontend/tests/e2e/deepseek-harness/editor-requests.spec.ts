import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectNoNativeEditorRequest } from '../helpers/unsupportedEditor'
import { nativeContext } from './scenarios'

deepseekHarnessTest('completes a native tool without a multiline editor control', async ({ deepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: deepseekHarnessWorkspace.workspaceId })
  await expectNoNativeEditorRequest(context, { relatedControl: () => exerciseShellToolExecution(context, { includeFailure: false }) })
})
