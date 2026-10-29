import { exerciseChildInterrupt, HELD_CHILD_TASK } from '../helpers/subagentRegistry'
import { qoderTest } from '../qoder-fixtures'
import { qoderChildTurn } from './childScenario'

// Qoder stops the child's task with stop_task. Its notification states `stopped`, and the parent reads the child's
// cancelled tool result and runs its next turn.
qoderTest('interrupts the native child task without stopping its parent', async ({ native }) => {
  await exerciseChildInterrupt(native, {
    childTurn: qoderChildTurn(HELD_CHILD_TASK),
    finalStatus: 'stopped',
  })
})
