import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { junieAnswerToolCall, junieSubagentSubmitToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectRowBecomesFinal, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { assistantBubbles, openWorkspace, sendMessage, tabById, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { expect, JUNIE_AGENT, junieTest } from '../junie-fixtures'

junieTest.describe('Junie subagents and background tasks', () => {
  const PROVIDER = AgentProvider.JUNIE

  const CHILD_TASK = 'Use the bundled Junie docs to explain where Junie stores session history.'

  const CHILD_GATE = 'junie-docs-submit'

  function junieHousekeeping() {
    return [
      { name: 'junie-capability-filter', when: { system: 'capability filter agent' }, respond: { text: '' } },
      { name: 'junie-task-name', when: { system: 'task description summarizer' }, respond: { text: 'Subagent task' } },
      { name: 'junie-task-summary', when: { system: 'You are a task summarizer' }, respond: { text: '<summary>Junie stores sessions in Junie Home.</summary><title>Session history</title>' } },
    ]
  }

  junieTest('routes the child task and report into a tab opened from the registry row', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { agentId } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, JUNIE_AGENT)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const childPrompt = modelScript.prompt(CHILD_TASK)

    await modelScript.rule(...junieHousekeeping())
    await modelScript.rule({
      name: 'the docs child submits its answer',
      when: { system: 'You are the Junie documentation assistant', body: CHILD_TASK },
      respond: {
        gate: CHILD_GATE,
        toolCalls: [junieSubagentSubmitToolCall('junie-child-submit', '### Summary\n- JUNIE_CHILD_DONE: Junie keeps its session history in its home.\n### Changes\n- No files changed.\n### Verification\n- Read the bundled documentation.')],
      },
      once: true,
    })
    await modelScript.queue(
      {
        toolCalls: [spawnSubagentToolCall(PROVIDER, 'spawn-junie', {
          description: 'Find Junie session history',
          prompt: childPrompt,
        })],
      },
      { toolCalls: [junieAnswerToolCall('junie-root-answer', 'JUNIE_ROOT_DONE')] },
    )
    await sendMessage(page, modelScript.prompt('Delegate the Junie session history question, then report.'))
    await modelScript.waitForGate(CHILD_GATE)
    try {
      const row = await requireRegistryRow(page)
      await expect(row).toHaveAttribute('data-status', 'running')
      await expect(row).toContainText('junie-cli-docs')
      await openChildTabFromRow(page, row)
      await expect(userBubbles(page).filter({ hasText: CHILD_TASK })).toHaveCount(1)
    }
    finally {
      await modelScript.releaseGate(CHILD_GATE)
    }

    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'JUNIE_CHILD_DONE' }).first()).toBeVisible()

    await tabById(page, agentId).click()
    await expect(assistantBubbles(page).filter({ hasText: 'JUNIE_ROOT_DONE' }).first()).toBeVisible()
    await expectRowBecomesFinal(page, await requireRegistryRow(page))
  })
})
