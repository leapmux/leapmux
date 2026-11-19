import { expect } from '@playwright/test'
import { exerciseChildInterrupt, HELD_CHILD_TASK, HELD_CHILD_TITLE } from '../helpers/subagentRegistry'
import { opencodeTest } from '../opencode-fixtures'
import { OPENCODE_CHILD, openCodeChildReportRule } from './childScenario'

// The Worker interrupts the native child session. Its cancellation sends a separate report that wakes the parent.
opencodeTest('interrupts the native child session without stopping its parent', async ({ native }) => {
  const report = openCodeChildReportRule(HELD_CHILD_TITLE)
  await exerciseChildInterrupt(native, {
    beforeRelease: async () => { await native.modelScript.rule(report) },
    childTurn: OPENCODE_CHILD.childTask(HELD_CHILD_TASK),
  })
  expect((await native.modelScript.status()).ruleMatches[report.name] ?? 0).toBeGreaterThan(0)
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/518.
