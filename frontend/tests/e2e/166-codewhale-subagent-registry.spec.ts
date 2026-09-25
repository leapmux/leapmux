import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEWHALE_E2E_SKIP_REASON, CODEWHALE_SERVES_JOB_ROUTES, codewhaleTest, expect } from './codewhale-fixtures'
import { backgroundBashToolCall, spawnSubagentToolCall } from './helpers/providerToolCalls'
import {
  expectNoRegistryRows,
  expectRowBecomesFinal,
  expectSectionPersists,
  openChildTabFromRow,
  requireRegistryRow,
} from './helpers/subagentRegistry'
import { assistantBubbles, sendMessage, userBubbles } from './helpers/ui'

/**
 * 166 -- Codewhale subagent registry and child transcript.
 *
 * The `agent` tool returns at once with the child's id, and the child runs in
 * the background. The runtime reports nothing about the child on the thread's
 * own stream, so the worker reads the child's transcript file and its run
 * record until the run ends. When the child ends, the runtime also starts a
 * parent turn of its own that hands the child's answer to the model.
 */
codewhaleTest.skip(!!CODEWHALE_E2E_SKIP_REASON, CODEWHALE_E2E_SKIP_REASON || '')

codewhaleTest.describe('Codewhale subagent registry', () => {
  codewhaleTest('a spawned child gets a registry row, a transcript tab and a report', async ({
    authenticatedCodewhaleWorkspace,
    page,
    modelScript,
  }) => {
    void authenticatedCodewhaleWorkspace
    await expectNoRegistryRows(page)

    // The child's prompt carries the marker, so the child's own turns reach
    // this script, and this rule answers them.
    await modelScript.rule({
      name: 'the child answers its one-word task',
      when: { user: 'Reply with the single word PONG' },
      respond: { text: 'PONG' },
    })
    await modelScript.queue({
      toolCalls: [spawnSubagentToolCall(AgentProvider.CODEWHALE, 'spawn-codewhale', {
        description: 'Ask for one word',
        prompt: modelScript.prompt('Reply with the single word PONG.'),
      })],
    })
    // Every parent turn after the spawn. How many there are is the runtime's
    // choice: the child can end before the parent's next request, and then its
    // answer joins that request, or after it, and then the runtime starts a new
    // parent turn to deliver it.
    await modelScript.fallback({ text: 'The subagent reported PONG.' })
    await sendMessage(page, modelScript.prompt('Delegate one word to a read-only subagent.'))
    await modelScript.waitForSteps(1)

    // The spawn is scripted, so a missing row is a failure rather than the
    // model's discretion.
    const row = await requireRegistryRow(page)
    await expect(row).toContainText('ask_for_one_word')
    await expectRowBecomesFinal(page, row)
    await expect(row).toHaveAttribute('data-status', 'completed')
    await expectSectionPersists(page)

    // The child's summary reaches the parent transcript under the child's name.
    await expect(page.locator('[data-testid="message-bubble"]:visible')
      .filter({ hasText: 'ask_for_one_word reported' })
      .filter({ hasText: /PONG/ })).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'The subagent reported PONG.' }).first()).toBeVisible()

    // The child transcript opens on the child's prompt, and then holds the
    // child's own answer.
    await expect.poll(async () => await row.getAttribute('data-child-agent-id')).not.toBe('')
    await openChildTabFromRow(page, row)
    await expect(userBubbles(page).filter({ hasText: 'Reply with the single word PONG.' })).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: /^PONG$/ })).toBeVisible()
  })

  codewhaleTest('a background shell job gets a shell row, which closes when the job ends', async ({
    authenticatedCodewhaleWorkspace,
    page,
    modelScript,
  }) => {
    void authenticatedCodewhaleWorkspace
    await expectNoRegistryRows(page)

    // `task_shell_start` is a deferred tool: the first call loads its schema and
    // runs nothing, and the second one starts the job.
    const start = backgroundBashToolCall(AgentProvider.CODEWHALE, 'load-shell', 'echo codewhale-bg-done')
    await modelScript.queue(
      { toolCalls: [start] },
      { toolCalls: [{ ...start, id: 'start-shell' }] },
      { text: 'The job runs in the background.' },
    )
    // The runtime may hand the job's end to the model in a turn of its own.
    await modelScript.fallback({ text: 'The job ended.' })
    await sendMessage(page, modelScript.prompt('Run the echo in the background.'))
    await modelScript.waitForSteps(2)

    // The Ask posture asks before the job's command runs. The schema load ran
    // nothing, so it asked nothing.
    const banner = page.locator('[data-testid="control-banner"]')
    await expect(banner).toBeVisible()
    await expect(banner).toContainText('echo codewhale-bg-done')
    await page.getByTestId('control-allow-btn').click()
    await expect(banner).not.toBeVisible()
    await modelScript.waitForSteps(3)

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
