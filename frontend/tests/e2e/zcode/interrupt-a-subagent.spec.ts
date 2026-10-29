import { exerciseChildInterrupt, HELD_CHILD_TASK } from '../helpers/subagentRegistry'
import { zcodeTest } from '../zcode-fixtures'
import { ZCODE_CHILD } from './childScenario'

// ZCode runs a background subagent as a task of its session, and the Interrupt control of the subagent's tab cancels
// that task alone. The spawn returns at once, so the parent answers before the stop, and the stop's notice runs the
// parent once more. A foreground subagent runs inside its parent's turn, and ZCode stops it only with that turn.
zcodeTest('the Interrupt control of a background subagent\'s tab stops that subagent alone', async ({ native }) => {
  await ZCODE_CHILD.prepare?.(native)
  await exerciseChildInterrupt(native, {
    childTurn: ZCODE_CHILD.childTask(HELD_CHILD_TASK),
    background: true,
    rootTurnsBeforeStop: [
      { text: 'The subagent counts in the background.' },
    ],
  })
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/518.
