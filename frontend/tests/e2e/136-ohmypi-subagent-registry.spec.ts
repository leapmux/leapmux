import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { ohMyPiYieldToolCall, spawnSubagentToolCall } from './helpers/providerToolCalls'
import {
  expectNoRegistryRows,
  expectRowBecomesFinal,
  expectSectionPersists,
  openChildTabFromRow,
  requireRegistryRow,
} from './helpers/subagentRegistry'
import { sendMessage, userBubbles } from './helpers/ui'
import { expect, OH_MY_PI_E2E_SKIP_REASON, ohMyPiTest } from './ohmypi-fixtures'

/**
 * 136 — Oh My Pi subagent registry.
 *
 * omp's `task` tool starts one subagent for each entry of its `tasks` list, and
 * reports each through `subagent_lifecycle`, `subagent_progress` and
 * `subagent_event`. The worker gives each subagent a registry row and a child
 * transcript. A subagent ends by calling `yield`, and what it yields is the
 * report that its row and its transcript state.
 *
 * The E2E profile turns omp's background tasks off, so the `task` call waits for
 * its subagent and the parent's run continues when the subagent ends.
 */
ohMyPiTest.skip(!!OH_MY_PI_E2E_SKIP_REASON, OH_MY_PI_E2E_SKIP_REASON || '')

/** What the subagent yields, and what its row and its transcript report. */
const REPORT = 'Apple, banana, cherry. One, two, three. Done.'

ohMyPiTest.describe('Oh My Pi subagent registry', () => {
  ohMyPiTest('follows a subagent from its spawn to its report', async ({ authenticatedOhMyPiWorkspace, page, modelScript }) => {
    void authenticatedOhMyPiWorkspace
    await expectNoRegistryRows(page)

    // omp opens a subagent's conversation with this line and the task text. The
    // task carries the marker, so the subagent's turns reach this script. The
    // line is not at the start of the user text, because omp puts a
    // `<system-reminder>` block before every prompt.
    //
    // The `yield` tool is the second condition, and the one that keeps the rule
    // off the parent: omp offers that tool to a subagent alone, so a request
    // that lists it is a subagent's turn.
    await modelScript.rule({
      name: 'the subagent yields its report',
      when: { user: 'Complete assignment thoroughly', body: '"name":"yield"' },
      respond: { toolCalls: [ohMyPiYieldToolCall('yield-report', REPORT)] },
    })
    await modelScript.queue(
      {
        toolCalls: [spawnSubagentToolCall(AgentProvider.OH_MY_PI, 'spawn-omp', {
          description: 'Run the fruit task',
          prompt: modelScript.prompt('List three fruits, then count to three, then report done.'),
        })],
      },
      { text: 'The subagent listed three fruits and counted to three.' },
    )
    await sendMessage(page, modelScript.prompt('Spawn one subagent for the counting task and report what it says.'))

    // The spawn is scripted, so a missing row is a failure rather than the
    // model's choice.
    const row = await requireRegistryRow(page)
    // The row's title is omp's id for the subagent, which is the task's name.
    await expect(row).toContainText('run_the_fruit_task')
    await modelScript.waitForSteps()

    await expectRowBecomesFinal(page, row)
    await expectSectionPersists(page)
    expect((await modelScript.status()).ruleMatches['the subagent yields its report']).toBeGreaterThan(0)
    // `getAttribute` answers null for an absent attribute, and null is not '', so the
    // poll reads an absent attribute as the empty id it states.
    await expect.poll(async () => await row.getAttribute('data-child-agent-id') ?? '').not.toBe('')

    await openChildTabFromRow(page, row)
    await expect(userBubbles(page).filter({ hasText: /list three fruits/i })).toBeVisible()
    // The report states the subagent by its id, as its row does, and carries
    // what the subagent yielded.
    await expect(page.locator('[data-testid="message-bubble"]:visible')
      .filter({ hasText: 'run_the_fruit_task reported' })
      .filter({ hasText: REPORT })).toBeVisible()
  })
})
