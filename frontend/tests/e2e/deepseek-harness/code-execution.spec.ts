import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseNativeCodeExecution, nativeCodeExecutionSchema } from '../helpers/nativeCodeExecution'

deepseekHarnessTest('runs actual native JavaScript source and keeps its computed output and failure after reload', async ({ native }) => {
  await exerciseNativeCodeExecution(native, {
    scripts: marker => [
      { label: 'computed', source: `return ${JSON.stringify(marker)} + String(21 * 2)`, expected: `${marker}42`, failed: false },
      { label: 'failed', source: `throw new Error(${JSON.stringify(marker)} + String(4 + 3))`, expected: `${marker}7`, failed: true },
    ],
    catalogProof: (request) => { nativeCodeExecutionSchema(request, 'workflow', { script: 'string', meta: 'object' }) },
  })
})
