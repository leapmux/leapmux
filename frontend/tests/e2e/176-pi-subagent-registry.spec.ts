import {
  expectRegistryOnlySubagentEnds,
  expectRegistrySectionAbsent,
  waitForRegistryRow,
} from './helpers/subagentRegistry'
import { sendMessage } from './helpers/ui'
/**
 * 176 — Pi subagent registry (pi-subagents extension).
 *
 * Pi's tool_execution_update details feed a LIVE activity line. Foreground: a
 * running row whose activity text changes over time. Background: after the
 * Agent tool's own result renders in the parent transcript, the row stays
 * running (background re-key by agent id) until a subagent-notification
 * message closes it. Registry-only: not clickable, no child agents.
 */
import { PI_E2E_SKIP_REASON, piTest } from './pi-fixtures'

piTest.skip(!!PI_E2E_SKIP_REASON, PI_E2E_SKIP_REASON || '')

piTest.describe('Pi subagent registry', () => {
  piTest('foreground subagent shows a live activity row', async ({
    authenticatedPiWorkspace,
    page,
  }) => {
    void authenticatedPiWorkspace

    await expectRegistrySectionAbsent(page)

    // A multi-step foreground subagent task.
    await sendMessage(page, 'Use the Agent tool to spawn a subagent that lists three fruits, then counts to five, then reports done.')

    // Wait for the row directly (waitForAgentIdle now blocks while a task is
    // active, since the indicator stays up for an active task count).
    const row = await waitForRegistryRow(page)

    await expectRegistryOnlySubagentEnds(page, row, { bestEffort: true })
  })
})
