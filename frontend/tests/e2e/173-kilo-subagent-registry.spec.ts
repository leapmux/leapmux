import {
  expectRegistryOnlySubagentEnds,
  expectRegistrySectionAbsent,
  requireRegistryRow,
} from './helpers/subagentRegistry'
import { sendMessage, waitForAgentIdle } from './helpers/ui'
/**
 * 173 — KiloCode subagent registry (registry-only, same ACP layer as OpenCode).
 *
 * IMPORTANT: Kilo's default model is an image model that no-ops agentic turns;
 * the kilo fixture opens the agent with an explicit text-capable model so the
 * subagent spawn actually runs.
 */
import { KILO_E2E_SKIP_REASON, kiloTest } from './kilo-fixtures'

kiloTest.skip(!!KILO_E2E_SKIP_REASON, KILO_E2E_SKIP_REASON || '')

kiloTest.describe('Kilo subagent registry', () => {
  kiloTest('subagent spawn creates a registry row with no child transcript', async ({
    authenticatedKiloWorkspace,
    page,
  }) => {
    void authenticatedKiloWorkspace

    await expectRegistrySectionAbsent(page)

    await sendMessage(page, 'Use your task tool to spawn a subagent that runs `echo kilo-done` and reports the result.')
    await waitForAgentIdle(page, 180_000)

    // The model may choose not to spawn; skip the spawn-dependent assertions
    // rather than fail on a real LLM's discretion.
    const row = await requireRegistryRow(kiloTest, page)

    await expectRegistryOnlySubagentEnds(page, row)
  })
})
