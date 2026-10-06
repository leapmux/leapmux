/** Test the child prompt and file result before the child finishes. */
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { AgentProvider, BackgroundTaskStatus, MessageSource } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { claudeTest } from '../claude-fixtures'
import { exerciseLiveChildTranscript } from '../helpers/liveChildTranscript'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { tabById } from '../helpers/ui'
import { registerClaudeChildReportRules } from './childReportRule'

/** Check the actual forwarded child answer and every stored byte after reload. */
async function expectNativeChildCompletion(context: Pick<ManagedNativeScenarioContext, 'page' | 'leapmuxServer'>): Promise<void> {
  const child = await currentNativeAgent(context)
  expect(child.parentAgentId).not.toBe('')
  await retryUntilPass(async () => {
    const parent = await readNativeSidebarSnapshot(context, child.parentAgentId)
    expect(parent.backgroundTasks.find(task => task.childAgentId === child.id)?.status, 'the Worker completes the task of the child')
      .toBe(BackgroundTaskStatus.COMPLETED)
  })
  const snapshot = await readNativeMessageSnapshot(context, child.id)
  const answers = snapshot.messages.filter((message) => {
    const body = nativeMessageBody(message)
    const content = isObject(body) && isObject(body.message) ? body.message.content : undefined
    return message.source === MessageSource.AGENT && isObject(body) && body.type === 'assistant'
      && Array.isArray(content) && content.some(block => isObject(block) && block.type === 'text' && block.text === 'CHILD_LIVE_DONE')
  })
  expect(answers).toHaveLength(1)
  const answer = answers[0]
  if (!answer)
    throw new Error('The actual forwarded child answer is absent.')
  expect(nativeMessageBody(answer)).toMatchObject({ parent_tool_use_id: child.spawnSpanId })
  expect(snapshot.messages.map(nativeMessageBody).filter(body => isObject(body) && body.type === 'subagent_ended')).toHaveLength(0)
  await expect(context.page.locator('[data-testid="thinking-indicator"]:visible')).toHaveCount(0)
  await context.page.reload()
  await tabById(context.page, child.id).click()
  await expect(context.page.locator('[data-testid="thinking-indicator"]:visible')).toHaveCount(0)
  const reopened = await readNativeMessageSnapshot(context, child.id)
  expect(reopened.messages).toEqual(snapshot.messages)
}

claudeTest.describe('Claude subagent background tasks', () => {
  claudeTest('shows a child prompt while that child still waits for its model', async ({ authenticatedWorkspace, page, modelScript, leapmuxServer }) => {
    void authenticatedWorkspace
    await exerciseLiveChildTranscript({ page, modelScript, leapmuxServer, provider: AgentProvider.CLAUDE_CODE }, {
      childWhen: { user: 'CHILD_LIVE_CLAUDE_MARKER' },
      childTask: 'Report CHILD_LIVE_CLAUDE_MARKER.',
      parentTask: 'Spawn one subagent to report its assigned marker.',
      beforeRelease: async () => {
        await registerClaudeChildReportRules(modelScript, { spawnCallId: 'spawn-live-child', report: 'CHILD_LIVE_DONE', reply: 'The live child report arrived.', completionStatus: 'completed', completionReply: 'The native child completion notification arrived.' })
      },
      afterComplete: () => expectNativeChildCompletion({ page, leapmuxServer }),
    })
  })

  claudeTest('shows a child file result only in the running child tab', async ({ authenticatedWorkspace, page, modelScript, leapmuxServer }) => {
    void authenticatedWorkspace
    const agent = await currentNativeAgent({ page, leapmuxServer })
    expect(agent.agentProvider).toBe(AgentProvider.CLAUDE_CODE)
    const workingDir = agent.workingDir
    if (!workingDir)
      throw new Error('The live child file proof requires a working directory.')
    await exerciseLiveChildTranscript({ page, modelScript, leapmuxServer, provider: AgentProvider.CLAUDE_CODE }, {
      childWhen: { user: 'CHILD_LIVE_CLAUDE_READ' },
      childTask: 'Read the assigned file for CHILD_LIVE_CLAUDE_READ.',
      parentTask: 'Start one child to read the assigned file.',
      toolProof: { read: { workingDir } },
      beforeRelease: async () => {
        await registerClaudeChildReportRules(modelScript, { spawnCallId: 'spawn-live-child', report: 'CHILD_LIVE_DONE', reply: 'The child file report arrived.', completionStatus: 'completed', completionReply: 'The native child completion notification arrived.' })
      },
      afterComplete: () => expectNativeChildCompletion({ page, leapmuxServer }),
    })
  })
})
