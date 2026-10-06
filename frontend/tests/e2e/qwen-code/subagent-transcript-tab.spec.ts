import type { Page } from '@playwright/test'
import type { ModelScript } from '../helpers/modelScriptFixture'
import { expect } from '@playwright/test'
import { OPTION_ID_PERMISSION_MODE } from '../../../src/components/chat/settingsGroups'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { stepRequest } from '../helpers/mockModelScript'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectNoRegistryRows, expectRowBecomesFinal, expectSectionPersists, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { assistantBubbles, messageBubbles, openWorkspace, sendMessage, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { QWEN_AGENT, qwenTest } from '../qwen-fixtures'

/**
 * An actual native child opens its own transcript tab from the registry row. The tab must show the child's prompt and report.
 *
 * The Worker drives Qwen Code through the Agent Client Protocol.
 *
 * Qwen tags foreground updates with the spawning tool call. Background children publish no stream, so the Worker reads their transcript files. The native task cancel stops a selected child.
 */
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
  // `openChildTabFromRow` waits until the row links a child agent.
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
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, QWEN_AGENT, { optionValues: { [OPTION_ID_PERMISSION_MODE]: 'yolo' } })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectNoRegistryRows(page, leapmuxServer)
    await answerTheChild(modelScript)
    const start = await modelScript.queue(
      {
        toolCalls: [spawnSubagentToolCall(AgentProvider.QWEN_CODE, 'spawn-qwen', {
          description: 'Ask for one word',
          prompt: modelScript.prompt(`${CHILD_TASK}.`),
        })],
      },
      { text: 'The subagent reported PONG.' },
    )
    await sendMessage(page, modelScript.prompt('Delegate one word to a subagent.'))
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    await expect(messageBubbles(page).filter({ hasText: 'Agent "Ask for one word" completed' }).first()).toBeVisible()
    await expectChildTranscript(page)
  })

  qwenTest('a background subagent opens a row, fills its transcript from the file and ends with Qwen\'s own turn', async ({
    page,
    authenticatedEmptyWorkspace,
    leapmuxServer,
    modelScript,
  }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, QWEN_AGENT, { optionValues: { [OPTION_ID_PERMISSION_MODE]: 'yolo' } })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectNoRegistryRows(page, leapmuxServer)
    await answerTheChild(modelScript)
    const start = await modelScript.queue(
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
    const status = await modelScript.waitForSteps(start + 3)
    await waitForAgentIdle(page)
    expect(JSON.stringify(stepRequest(status, start + 2).body)).toContain('<task-notification>')
    await expect(assistantBubbles(page).filter({ hasText: 'The background subagent reported PONG.' })).toBeVisible()
    await expectChildTranscript(page)
  })
})
