import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { backgroundBashToolCall, junieAnswerToolCall, spawnSubagentToolCall } from './helpers/providerToolCalls'
import {
  expectRowBecomesFinal,
  openChildTabFromRow,
  requireRegistryRow,
} from './helpers/subagentRegistry'
import { assistantBubbles, messageBubbles, openWorkspace, sendMessage, userBubbles, waitForAgentIdle } from './helpers/ui'
import { expect, JUNIE_E2E_SKIP_REASON, junieTest, openJunieAgent } from './junie-fixtures'

junieTest.skip(!!JUNIE_E2E_SKIP_REASON, JUNIE_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.JUNIE

/** The task the child performs. A rule on the child's user turn answers it alone. */
const CHILD_TASK = 'Count the files and report the number.'

/** Housekeeping turns every Junie task answers before the main agent runs. */
function junieHousekeeping() {
  return [
    { name: 'junie-capability-filter', when: { system: 'capability filter agent' }, respond: { text: '' } },
    { name: 'junie-task-name', when: { system: 'task description summarizer' }, respond: { text: 'Subagent task' } },
  ]
}

junieTest.describe('Junie subagents and background tasks', () => {
  // `spawn_subagent` blocks on the child and returns its result. The registry
  // row opens the child's transcript in a tab of its own.
  junieTest('routes the child task and report into a tab opened from the registry row', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openJunieAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

    await modelScript.rule(...junieHousekeeping())
    await modelScript.rule({
      name: 'the child reports its count',
      when: { user: CHILD_TASK },
      respond: { toolCalls: [junieAnswerToolCall('junie-child-answer', 'JUNIE_CHILD_DONE')] },
    })
    await modelScript.queue(
      {
        toolCalls: [spawnSubagentToolCall(PROVIDER, 'spawn-junie', {
          description: 'Count the files',
          prompt: CHILD_TASK,
        })],
      },
      { toolCalls: [junieAnswerToolCall('junie-root-answer', 'JUNIE_ROOT_DONE')] },
    )
    await sendMessage(page, modelScript.prompt('Delegate the count to a subagent, then report.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    await expect(assistantBubbles(page).filter({ hasText: 'JUNIE_ROOT_DONE' }).first()).toBeVisible()

    const row = await requireRegistryRow(page)
    await expectRowBecomesFinal(page, row)
    await expect(row).toContainText('Count the files')
    await openChildTabFromRow(page, row)

    await expect(userBubbles(page).filter({ hasText: CHILD_TASK })).not.toHaveCount(0)
    await expect(assistantBubbles(page).filter({ hasText: 'JUNIE_CHILD_DONE' })).not.toHaveCount(0)
  })

  // The same `bash` tool with the `background` flag opens a SHELL row, which is
  // a different registry row kind from a subagent.
  junieTest('a background command opens a shell row that ends', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openJunieAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

    await modelScript.rule(...junieHousekeeping())
    await modelScript.queue(
      { toolCalls: [backgroundBashToolCall(PROVIDER, 'junie-bg', 'sleep 1; echo junie-background-done')] },
      { toolCalls: [junieAnswerToolCall('junie-bg-answer', 'I started the command in the background.')] },
    )
    await sendMessage(page, modelScript.prompt('Run the command in the background.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    const row = await requireRegistryRow(page, 'shell')
    await expectRowBecomesFinal(page, row)
    await expect(messageBubbles(page).filter({ hasText: 'junie-background-done' }).first()).toBeVisible()
  })
})
