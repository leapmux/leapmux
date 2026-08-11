import {
  expectRegistryOnlySubagentEnds,
  expectRegistrySectionAbsent,
  tryWaitForRegistryRow,
} from './helpers/subagentRegistry'
import { sendMessage } from './helpers/ui'
/**
 * 172 — OpenCode subagent registry (registry-only).
 *
 * OpenCode's ACP bridge forwards no child-session content, so this is
 * registry-only: a running row with the spawn title while the subagent works,
 * a terminal end label on completion, no clickable row, and no child agent
 * rows on the worker.
 */
import { OPENCODE_E2E_SKIP_REASON, opencodeTest } from './opencode-fixtures'

opencodeTest.skip(!!OPENCODE_E2E_SKIP_REASON, OPENCODE_E2E_SKIP_REASON || '')

opencodeTest.describe('OpenCode subagent registry', () => {
  opencodeTest('subagent spawn creates a registry row with no child transcript', async ({
    authenticatedOpencodeWorkspace,
    page,
  }) => {
    void authenticatedOpencodeWorkspace

    await expectRegistrySectionAbsent(page)

    await sendMessage(page, 'Use your task tool to spawn one subagent whose prompt is: reply with the single word PONG. Report what it said.')

    // Registry row appears (subagent, running while it works). The model may
    // choose not to spawn; in that case skip the spawn-dependent assertions.
    const row = await tryWaitForRegistryRow(page)
    opencodeTest.skip(!row, 'model did not spawn a subagent')
    const r = row!

    // Registry-only tail: final end label (best-effort -- this spec does not
    // wait for idle first), section persists, row not clickable, no child agent.
    await expectRegistryOnlySubagentEnds(page, r, { bestEffort: true })
  })
})
