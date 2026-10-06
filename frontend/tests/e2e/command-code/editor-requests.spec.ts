import { commandCodeTest } from '../command-code-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectNoNativeEditorRequest } from '../helpers/unsupportedEditor'
import { nativeContext } from './scenarios'

commandCodeTest('completes an actual native tool without a multiline editor request', async ({ authenticatedCommandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCommandCodeWorkspace.workspaceId })
  await expectNoNativeEditorRequest(context, { relatedProof: () => exerciseShellToolExecution(context, { includeFailure: false }) })
})
