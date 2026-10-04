import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseNativeCodeExecution, nativeCodeExecutionSchema } from '../helpers/nativeCodeExecution'
import { nativeContext } from './scenarios'

deepseekHarnessTest('runs actual native JavaScript source and keeps its computed output and failure after reload', async ({ deepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: deepseekHarnessWorkspace.workspaceId })
  await exerciseNativeCodeExecution(context, {
    scripts: marker => [
      { label: 'computed', source: `return ${JSON.stringify(marker)} + String(21 * 2)`, expected: `${marker}42`, failed: false },
      { label: 'failed', source: `throw new Error(${JSON.stringify(marker)} + String(4 + 3))`, expected: `${marker}7`, failed: true },
    ],
    catalogProof: (request) => { nativeCodeExecutionSchema(request, 'workflow', { script: 'string', meta: 'object' }) },
  })
})
