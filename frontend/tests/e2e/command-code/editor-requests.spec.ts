import { commandCodeTest } from '../command-code-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectNoNativeEditorRequest } from '../helpers/unsupportedEditor'

commandCodeTest('completes an actual native tool without a multiline editor request', async ({ native }) => {
  await expectNoNativeEditorRequest(native, { relatedProof: () => exerciseShellToolExecution(native, { includeFailure: false }) })
})
