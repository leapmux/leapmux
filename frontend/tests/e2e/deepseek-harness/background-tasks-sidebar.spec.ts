import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { expectRunningChildCompletes } from '../helpers/runningChildProof'
import { runningChild } from './scenarios'

deepseekHarnessTest('keeps the actual native child in the sidebar from running through completion', async ({ native }) => {
  await expectRunningChildCompletes(await runningChild(native), { reload: true })
})
