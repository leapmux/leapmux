import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectNoNativeEditorRequest } from '../helpers/unsupportedEditor'

deepseekHarnessTest('completes a native tool without a multiline editor control', async ({ native }) => {
  await expectNoNativeEditorRequest(native, { relatedProof: () => exerciseShellToolExecution(native, { includeFailure: false }) })
})
