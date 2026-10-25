import { exerciseChildInterrupt, HELD_CHILD_TASK } from '../helpers/subagentRegistry'
import { opencodeTest } from '../opencode-fixtures'
import { OPENCODE_CHILD } from './childScenario'

// The Worker reads the child session of the task call from the parent's stored task part, and cancels that session
// through the daemon's session abort. The task then fails, and the parent reads that result and runs its next turn.
opencodeTest('interrupts the native child session without stopping its parent', async ({ native }) => {
  await exerciseChildInterrupt(native, { childTurn: OPENCODE_CHILD.childTask(HELD_CHILD_TASK) })
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/518.
