import { exerciseChildInterrupt, HELD_CHILD_TASK } from '../helpers/subagentRegistry'
import { kimiTest } from '../kimi-fixtures'
import { kimiChildTurn, prepareKimiChildRun } from './childScenario'

kimiTest.describe('runs Kimi Code subagents and background tasks', () => {
  kimiTest.beforeEach(async ({ native }) => {
    await prepareKimiChildRun(native.page)
  })

  // Kimi Code runs a subagent as a task of its session, and the Interrupt
  // control of the subagent's tab cancels that task alone.
  kimiTest('the Interrupt control of a working subagent\'s tab stops that subagent alone', async ({ native }) => {
    await exerciseChildInterrupt(native, {
      childTurn: kimiChildTurn(HELD_CHILD_TASK),
    })
  })
})
