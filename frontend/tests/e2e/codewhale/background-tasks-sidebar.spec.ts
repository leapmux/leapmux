import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEWHALE_SERVES_JOB_ROUTES, codewhaleTest } from '../codewhale-fixtures'
import { backgroundBashToolCall } from '../helpers/providerToolCalls'
import { expectNoRegistryRows, expectRowBecomesFinal, requireRegistryRow } from '../helpers/subagentRegistry'
import { answerControl, sendMessage, waitForControlBanner } from '../helpers/ui'

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

    // `task_shell_start` is a deferred tool: the first call loads its schema and
    // runs nothing, and the second one starts the job.
    const shellCall = backgroundBashToolCall(AgentProvider.CODEWHALE, 'load-shell', 'echo codewhale-bg-done')
    const start = await modelScript.queue(
      { toolCalls: [shellCall] },
      { toolCalls: [{ ...shellCall, id: 'start-shell' }] },
      { text: 'The job runs in the background.' },
    )
    // The runtime may hand the job's end to the model in a turn of its own.
    await modelScript.fallback({ text: 'The job ended.' })
    await sendMessage(page, modelScript.prompt('Run the echo in the background.'))
    await modelScript.waitForSteps(start + 2)

    // The Ask posture asks before the job's command runs. The schema load ran
    // nothing, so it asked nothing.
    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('echo codewhale-bg-done')
    await answerControl(page, 'allow')
    await expect(banner).not.toBeVisible()
    await modelScript.waitForSteps(start + 3)

    const row = await requireRegistryRow(page, 'shell')
    await expect(row).toContainText('echo codewhale-bg-done')
    // From 0.10.0 the worker reads the job, which states that the job ended. An
    // older runtime states it to the model alone.
    if (CODEWHALE_SERVES_JOB_ROUTES) {
      await expectRowBecomesFinal(page, row)
      await expect(row).toHaveAttribute('data-status', 'completed')
    }
  })
})
