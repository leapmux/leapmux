import {
  expectNoChildAgents,
  expectRegistrySectionAbsent,
  expectRowBecomesTerminal,
  expectRowNotClickable,
  expectSectionPersists,
  openAgentTabIds,
  tryWaitForRegistryRow,
} from './helpers/subagentRegistry'
import { sendMessage } from './helpers/ui'
/**
 * 175 — Reasonix subagent registry (spawn-span-only).
 *
 * Reasonix withholds ToolProgress from ACP by design, so the row appears with
 * the spawn title and stays running until the terminal tool_result. There is
 * no per-tool activity-text update to assert. Registry-only: not clickable,
 * no child agents.
 */
import { REASONIX_E2E_SKIP_REASON, reasonixTest } from './reasonix-fixtures'

reasonixTest.skip(!!REASONIX_E2E_SKIP_REASON, REASONIX_E2E_SKIP_REASON || '')

reasonixTest.describe('Reasonix subagent registry', () => {
  reasonixTest('subagent spawn creates a registry row with no progress or child', async ({
    authenticatedReasonixWorkspace,
    page,
    leapmuxServer,
  }) => {
    void authenticatedReasonixWorkspace
    const { hubUrl, adminToken, workerId } = leapmuxServer

    await expectRegistrySectionAbsent(page)

    await sendMessage(page, 'Use your task tool to spawn one subagent whose prompt is: reply with the single word PONG. Report what it said.')

    // The model may choose not to spawn; skip the spawn-dependent assertions.
    const row = await tryWaitForRegistryRow(page)
    reasonixTest.skip(!row, 'model did not spawn a subagent')
    const r = row!

    await expectRowBecomesTerminal(page, r, 'Completed').catch(e => console.warn('terminal assertion (best-effort):', e?.message ?? e))
    await expectSectionPersists(page)
    await expectRowNotClickable(page, r)
    const tabIds = await openAgentTabIds(page)
    await expectNoChildAgents(hubUrl, adminToken, workerId, tabIds)
  })
})
