import { diracTest } from '../dirac-fixtures'
import { expectRunningChildCompletes, HELD_NATIVE_CHILD_DESCRIPTION } from '../helpers/runningChildProof'
import { runningChild } from './scenarios'

diracTest('follows a native child from running to completed in the Background tasks sidebar', async ({ native }) => {
  await expectRunningChildCompletes(await runningChild(native), { rowText: HELD_NATIVE_CHILD_DESCRIPTION })
})
