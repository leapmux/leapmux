import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { cleanName } from '../../../src/lib/validate'
import { codewhaleTest } from '../codewhale-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { currentNativeAgent, nativeModelContextText } from '../helpers/nativeScenario'
import { createOutputGate } from '../helpers/outputGate'
import { backgroundBashToolCall } from '../helpers/providerToolCalls'
import { expectNoRegistryRows, expectRowBecomesFinal, requireRegistryRow } from '../helpers/subagentRegistry'
import { sendMessage } from '../helpers/ui'
import { runWithoutApprovals } from './toolScenarios'

/**
 * Actual native tasks create registry rows. The test checks their running and completed states.
 *
 * The Worker drives Codewhale's runtime API. Codewhale stores each thread and its tool results in its private native store.
 *
 * The agent tool returns a child ID at once. Codewhale omits child events from the parent stream. The Worker reads the child transcript and run record until the run ends.
 */
codewhaleTest.describe('Codewhale subagent registry', () => {
  codewhaleTest('a background shell job gets a shell row, which closes when the job ends', async ({ native }) => {
    const { page, modelScript, leapmuxServer } = native
    await expectNoRegistryRows(page, leapmuxServer)

    await runWithoutApprovals(page)
    const agent = await currentNativeAgent(native)
    const gate = createOutputGate(agent.workingDir)
    const command = gate.hold('echo codewhale-bg-$((40 + 2))')
    await withCleanup(async () => {
      const start = await modelScript.queue(
        {
          toolCalls: [
            backgroundBashToolCall(AgentProvider.CODEWHALE, 'start-shell', command),
          ],
        },
        { text: 'The job runs in the background.' },
      )
      await modelScript.fallback({ text: 'The job ended.' })
      await sendMessage(page, modelScript.prompt('Run the held command in the background.'))
      await modelScript.waitForSteps(start + 2)
      const row = await requireRegistryRow(page, 'shell')
      await expect(row.getByTestId('bg-task-title')).toHaveText(cleanName(command))
      await expect(row).toHaveAttribute('data-status', 'running')
      expect(nativeModelContextText(await modelScript.requestAt(start + 1))).toContain('codewhale-bg-42')
      gate.release()
      await expectRowBecomesFinal(page, row)
      await expect(row).toHaveAttribute('data-status', 'succeeded')
    }, async () => gate.release())
  })
})
