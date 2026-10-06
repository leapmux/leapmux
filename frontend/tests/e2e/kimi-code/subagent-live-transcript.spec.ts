import { exerciseLiveChildTranscript } from '../helpers/liveChildTranscript'
import { applyPermissionPreset, expectSettingsChip, waitForSettingsHydrated } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'

/**
 * The statement that opens a Kimi Code subagent's system prompt, and that the
 * main agent's system prompt never holds. A rule on it answers the child alone,
 * although the root's own requests quote the child's prompt in the spawn call.
 */
const SUBAGENT_SYSTEM = 'You are now running as a subagent'

kimiTest.describe('runs Kimi Code subagents and background tasks', () => {
  kimiTest('shows the child prompt while the child still runs', async ({ native }) => {
    await exerciseLiveChildTranscript(native, {
      childWhen: { system: SUBAGENT_SYSTEM, body: 'CHILD_LIVE_DONE' },
      childTask: 'Reply with CHILD_LIVE_DONE.',
      parentTask: 'Delegate the live child task.',
    })
  })

  // A reminder follows the task as the last user turn of a child request, so the rule matches the task in the body.
  kimiTest('shows a native child file result only in the running child tab', async ({ native, authenticatedKimiWorkspace }) => {
    await exerciseLiveChildTranscript(native, {
      childWhen: { system: SUBAGENT_SYSTEM, body: 'CHILD_LIVE_READ_TASK' },
      childTask: 'Read the assigned file for CHILD_LIVE_READ_TASK.',
      parentTask: 'Delegate the live child file read.',
      toolProof: { read: { workingDir: authenticatedKimiWorkspace.workingDir } },
    })
  })

  // The child's command would stop at a banner under Always Ask. The routing,
  // not the approval, is the subject here.
  kimiTest.beforeEach(async ({ authenticatedKimiWorkspace, page }) => {
    void authenticatedKimiWorkspace
    await waitForSettingsHydrated(page)
    await applyPermissionPreset(page, 'bypass')
    await expectSettingsChip(page, 'Never Ask')
  })
})
