import { copilotTest } from '../copilot-fixtures'
import { exerciseChildInterrupt, HELD_CHILD_NAME, HELD_CHILD_TASK } from '../helpers/subagentRegistry'
import { copilotChildTaskMatcher } from './childIdentity'
import { bypassToolRequests } from './scenarios'

// Copilot tracks each subagent as a task of the session. The Worker cancels the task of the spawning call with
// session.tasks.cancel, and Copilot ends the subagent with a cancelled completion while the parent goes on.
copilotTest('interrupts the native child task without stopping its parent', async ({ native }) => {
  await bypassToolRequests(native)
  await exerciseChildInterrupt(native, {
    childTurn: copilotChildTaskMatcher(HELD_CHILD_TASK),
    rowTitle: HELD_CHILD_NAME,
    finalStatus: 'stopped',
  })
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/505.
