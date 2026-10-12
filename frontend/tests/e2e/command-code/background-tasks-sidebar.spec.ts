import { commandCodeTest } from '../command-code-fixtures'
import { expectRunningChildCompletes } from '../helpers/runningChildProof'
import { runningChild } from './scenarios'

commandCodeTest('follows an actual native child from running to completed', async ({ native }) => {
  await expectRunningChildCompletes(await runningChild(native))
})
