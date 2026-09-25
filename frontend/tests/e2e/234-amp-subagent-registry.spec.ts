import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { AMP_E2E_SKIP_REASON, ampTest, expect } from './amp-fixtures'
import { backgroundBashToolCall, spawnSubagentToolCall } from './helpers/providerToolCalls'
import {
  expectNoRegistryRows,
  expectRowBecomesFinal,
  expectSectionPersists,
  requireRegistryRow,
} from './helpers/subagentRegistry'
import { assistantBubbles, messageContents, sendMessage, waitForAgentIdle } from './helpers/ui'

/**
 * 234 — Amp subagent registry.
 *
 * Amp runs `Task` on its server, and its stream-JSON output drops every message of the
 * subagent: only the call and its final result reach stdout. So the worker opens a
 * registry row when the call starts and closes it when the result arrives, and the
 * row carries no child transcript. The mock's Amp surface runs the subagent as one
 * more inference, which the rule below answers.
 *
 * A shell command that outlives its call goes on in the background, and the worker
 * follows it in a shell row until Amp states its end or the Amp process exits.
 */
ampTest.skip(!!AMP_E2E_SKIP_REASON, AMP_E2E_SKIP_REASON || '')

/** What the subagent reports, which the parent's call result states. */
const REPORT = 'Apple, banana, cherry. One, two, three. Done.'

ampTest.describe('Amp subagent registry', () => {
  ampTest('follows a subagent from its call to its report', async ({ authenticatedAmpWorkspace, page, modelScript }) => {
    void authenticatedAmpWorkspace
    await expectNoRegistryRows(page)

    // The subagent's own inference holds its prompt alone, so the rule matches the
    // prompt's words. The parent's inferences never state them as the last user text.
    await modelScript.rule({
      name: 'the subagent reports',
      when: { user: 'List three fruits' },
      respond: { text: REPORT, delayMs: 3_000 },
    })
    await modelScript.queue(
      {
        toolCalls: [spawnSubagentToolCall(AgentProvider.AMP, 'spawn-amp', {
          description: 'Run the fruit task',
          prompt: modelScript.prompt('List three fruits, then count to three, then report done.'),
        })],
      },
      { text: 'The subagent listed three fruits and counted to three.' },
    )
    await sendMessage(page, modelScript.prompt('Spawn one subagent for the counting task and report what it says.'))

    // The test scripts the call, so a missing row is a failure and not the model's choice.
    const row = await requireRegistryRow(page)
    await expect(row).toContainText('Run the fruit task')
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expectRowBecomesFinal(page, row)
    await expect(row).toHaveAttribute('data-status', 'completed')
    await expectSectionPersists(page)
    expect((await modelScript.status()).ruleMatches['the subagent reports']).toBe(1)
    // Amp prints no message of the subagent, so the row opens no child transcript.
    expect(await row.getAttribute('data-child-agent-id') ?? '').toBe('')
    // The report reaches the parent's transcript as the call's result.
    await expect.poll(async () => (await messageContents(page).allTextContents()).join(' ')).toContain(REPORT)
  })

  ampTest('follows a background command until the Amp process exits', async ({ authenticatedAmpWorkspace, page, modelScript }) => {
    void authenticatedAmpWorkspace
    await expectNoRegistryRows(page)

    // The real Amp CLI runs the command, so the row proves the record that Amp returns
    // for a command that outlives its call.
    await modelScript.queue(
      { toolCalls: [backgroundBashToolCall(AgentProvider.AMP, 'bg-shell', 'sleep 120')] },
      { text: 'The command runs in the background.' },
    )
    await sendMessage(page, modelScript.prompt('Start the long command in the background.'))
    const row = await requireRegistryRow(page, 'shell')
    await expect(row).toContainText('sleep 120')
    await modelScript.waitForSteps()
    await expect(assistantBubbles(page).filter({ hasText: 'The command runs in the background.' })).not.toHaveCount(0)
    // The command outlives the turn that started it.
    await expect(row).toHaveAttribute('data-status', 'running')

    // Amp stops every command that it started when it exits, and an interrupt ends the
    // process. A held answer keeps the turn open until the interrupt.
    await modelScript.queue({ text: 'An essay.', delayMs: 60_000 })
    await sendMessage(page, modelScript.prompt('Write a long essay about the history of computing.'))
    await modelScript.waitForSteps(1)
    await page.locator('[data-testid="interrupt-button"]:visible').click()
    await expectRowBecomesFinal(page, row)
    await expect(row).toHaveAttribute('data-status', 'stopped')
  })
})
