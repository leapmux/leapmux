import { exerciseChildInterrupt, HELD_CHILD_TASK } from '../helpers/subagentRegistry'
import { applyPermissionPreset, expectSettingsChip, waitForSettingsHydrated } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'

/**
 * The statement that opens a Kimi Code subagent's system prompt, and that the
 * main agent's system prompt never holds. A rule on it answers the child alone,
 * although the root's own requests quote the child's prompt in the spawn call.
 */
const SUBAGENT_SYSTEM = 'You are now running as a subagent'

kimiTest.describe('runs Kimi Code subagents and background tasks', () => {
  // The child's command would stop at a banner under Always Ask. The routing,
  // not the approval, is the subject here.
  kimiTest.beforeEach(async ({ authenticatedKimiWorkspace, page }) => {
    void authenticatedKimiWorkspace
    await waitForSettingsHydrated(page)
    await applyPermissionPreset(page, 'bypass')
    await expectSettingsChip(page, 'Never Ask')
  })

  // Kimi Code runs a subagent as a task of its session, and the Interrupt
  // control of the subagent's tab cancels that task alone.
  kimiTest('the Interrupt control of a working subagent\'s tab stops that subagent alone', async ({ native }) => {
    await exerciseChildInterrupt(native, {
      childTurn: { system: SUBAGENT_SYSTEM, body: HELD_CHILD_TASK },
    })
  })
})
