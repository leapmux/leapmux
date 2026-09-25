import type { Page } from '@playwright/test'
import type { ModelScript } from './helpers/modelScriptFixture'
import { OPTION_ID_PERMISSION_MODE } from '../../src/components/chat/settingsGroups'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { spawnSubagentToolCall } from './helpers/providerToolCalls'
import {
  exerciseChildInterrupt,
  expectNoRegistryRows,
  expectRowBecomesFinal,
  expectSectionPersists,
  HELD_CHILD_TASK,
  openChildTabFromRow,
  requireRegistryRow,
} from './helpers/subagentRegistry'
import { assistantBubbles, messageBubbles, openWorkspace, sendMessage, userBubbles, waitForAgentIdle } from './helpers/ui'
import { expect, openQwenAgent, QWEN_E2E_SKIP_REASON, qwenTest } from './qwen-fixtures'

/**
 * 124 -- Qwen Code subagent registry.
 *
 * A FOREGROUND subagent streams into the parent session. Qwen tags each of its
 * updates with the tool call that spawned it, and the worker routes them into
 * the child's own transcript. A BACKGROUND subagent streams nothing: the worker
 * reads its transcript file instead, and Qwen reports its end with a turn of its
 * own. A spawn needs an approval in Qwen's `default` mode, which is not this
 * test's subject, so the agent runs in YOLO.
 *
 * The tab of a working subagent offers Interrupt, which stops that subagent
 * through Qwen's task cancel. Qwen reports the aborted subagent as failed, and
 * the worker closes its row as stopped, because the user asked for the stop.
 */
qwenTest.skip(!!QWEN_E2E_SKIP_REASON, QWEN_E2E_SKIP_REASON || '')

const CHILD_TASK = 'Reply with the single word PONG'

/** Answer the child's turn, which has no order the test controls against the parent's. */
async function answerTheChild(script: ModelScript): Promise<void> {
  await script.rule({
    name: 'the child answers its one-word task',
    when: { user: CHILD_TASK },
    respond: { text: 'PONG' },
  })
}

/** The row, and the child transcript it opens, carry the child's task and its answer. */
async function expectChildTranscript(page: Page): Promise<void> {
  const row = await requireRegistryRow(page)
  await expectRowBecomesFinal(page, row)
  await expectSectionPersists(page)
  await expect.poll(async () => await row.getAttribute('data-child-agent-id')).not.toBe('')
  await openChildTabFromRow(page, row)
  await expect(userBubbles(page).filter({ hasText: CHILD_TASK })).toBeVisible()
  await expect(assistantBubbles(page).filter({ hasText: 'PONG' }).first()).toBeVisible()
}

qwenTest.describe('Qwen Code subagent registry', () => {
  qwenTest('a foreground subagent opens a row and a child transcript with its report', async ({
    page,
    authenticatedEmptyWorkspace,
    leapmuxServer,
    modelScript,
  }) => {
    await openQwenAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { [OPTION_ID_PERMISSION_MODE]: 'yolo' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectNoRegistryRows(page)
    await answerTheChild(modelScript)
    await modelScript.queue(
      {
        toolCalls: [spawnSubagentToolCall(AgentProvider.QWEN_CODE, 'spawn-qwen', {
          description: 'Ask for one word',
          prompt: modelScript.prompt(`${CHILD_TASK}.`),
        })],
      },
      { text: 'The subagent reported PONG.' },
    )
    await sendMessage(page, modelScript.prompt('Delegate one word to a subagent.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    await expect(messageBubbles(page).filter({ hasText: 'Agent "Ask for one word" completed' }).first()).toBeVisible()
    await expectChildTranscript(page)
  })

  qwenTest('a background subagent opens a row, fills its transcript from the file and ends with Qwen\'s own turn', async ({
    page,
    authenticatedEmptyWorkspace,
    leapmuxServer,
    modelScript,
  }) => {
    await openQwenAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { [OPTION_ID_PERMISSION_MODE]: 'yolo' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectNoRegistryRows(page)
    await answerTheChild(modelScript)
    await modelScript.queue(
      {
        toolCalls: [spawnSubagentToolCall(AgentProvider.QWEN_CODE, 'spawn-qwen-background', {
          description: 'Ask for one word',
          prompt: modelScript.prompt(`${CHILD_TASK}.`),
          background: true,
        })],
      },
      { text: 'The subagent runs in the background.' },
      // The turn Qwen starts by itself when the subagent ends, with its report.
      { text: 'The background subagent reported PONG.' },
    )
    await sendMessage(page, modelScript.prompt('Delegate one word to a background subagent.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    const status = await modelScript.status()
    expect(JSON.stringify(status.requests.at(-1)?.body)).toContain('<task-notification>')
    await expect(assistantBubbles(page).filter({ hasText: 'The background subagent reported PONG.' })).toBeVisible()
    await expectChildTranscript(page)
  })

  qwenTest('the Interrupt control of a working subagent\'s tab stops that subagent alone', async ({
    page,
    authenticatedEmptyWorkspace,
    leapmuxServer,
    modelScript,
  }) => {
    await openQwenAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { [OPTION_ID_PERMISSION_MODE]: 'yolo' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectNoRegistryRows(page)
    // Qwen asks no session title for a child, so the task alone selects the
    // child's own turn.
    await exerciseChildInterrupt(page, modelScript, {
      provider: AgentProvider.QWEN_CODE,
      childTurn: { user: HELD_CHILD_TASK },
    })
  })
})
