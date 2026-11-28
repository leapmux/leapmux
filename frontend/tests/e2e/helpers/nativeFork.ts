import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { randomUUID } from 'node:crypto'
import { Code } from '@connectrpc/connect'
import { expect } from '@playwright/test'
import { OPTION_ID_MODEL } from '../../../src/components/chat/settingsGroups'
import { AgentStatus, MessageSource, WorkerAgentService } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { TabInsertion } from '../../../src/generated/proto/leapmux/v1/tab_creation_pb'
import { agentOpenOptions } from '../agentSettings'
import { getTestChannel } from './api'
import { countChatBubbles } from './chatBubbleCount'
import { getUserEventsSubscription } from './crdt'
import { isStoredMessageSeq, nativeMessagesHoldingText, readNativeMessageSnapshot } from './nativeMessages'
import { expectNativeResumeContext, expectUnmergedAnswerBubbles, nativeResumeTexts, readOriginalConversationRows } from './nativeResume'
import { currentNativeAgent, nativeAgentById, nativeModelConversationTurns, nativeTextStep } from './nativeScenario'
import { retryUntilPass } from './retryUntilPass'
import { uniqueMarker } from './shellArguments'
import { sendMessage, tabById, waitForAgentIdle } from './ui'

/** Build the OpenAgent options map for a provider's E2E fixture defaults. */
function defaultForkOptions(provider: AgentInfo['agentProvider']): Record<string, string> {
  const openOpts = agentOpenOptions(provider)
  return {
    ...openOpts.optionValues,
    ...(openOpts.model === undefined
      ? {}
      : { [OPTION_ID_MODEL]: openOpts.model }),
  }
}

/** Wait until the source native agent is active and has confirmed a non-empty native session ID. */
async function waitForActiveNativeSession(context: ManagedNativeScenarioContext, agentId: string): Promise<AgentInfo> {
  return retryUntilPass(async () => {
    const agent = await nativeAgentById(context, agentId)
    if (!agent)
      throw new Error(`The Worker holds no agent ${agentId}.`)
    expect(agent.status, 'The native agent must be ACTIVE before forking.').toBe(AgentStatus.ACTIVE)
    expect(agent.agentSessionId, 'The Worker must confirm a native session ID before forking.').not.toBe('')
    return agent
  })
}

/**
 * Exercise native conversation-history forking end-to-end on a supported provider:
 *
 * 1. Complete turn 1 on the source agent tab and confirm `supportsFork` and a native session ID.
 * 2. Fork the idle source agent via `OpenAgent` with `forkSource`, confirming a distinct native session ID
 *    and `forkedFromAgentId`.
 * 3. Send turn 2 on the forked tab and prove the native model request carries turn 1 in order while the
 *    UI keeps the cloned transcript unmerged.
 * 4. Send turn 3 on the original source tab and prove branch isolation: the source model request carries
 *    turn 1 and turn 3 without turn 2's prompt or answer.
 */
export async function exerciseNativeFork(context: ManagedNativeScenarioContext): Promise<void> {
  const { page, modelScript, leapmuxServer } = context
  const texts = nativeResumeTexts()
  const sourceFollowUpMarker = uniqueMarker()
  const sourceFollowUpPrompt = `Continue SOURCEBRANCH${sourceFollowUpMarker} on the original session.`
  const sourceFollowUpAnswer = `SOURCEBRANCHANSWER${sourceFollowUpMarker}`

  const initialAgent = await currentNativeAgent(context)
  const sourceId = initialAgent.id

  // Turn 1 on the source agent.
  const firstStep = await modelScript.queue(nativeTextStep(context, texts.originalAnswer))
  await sendMessage(page, modelScript.prompt(texts.originalPrompt))
  await modelScript.waitForSteps(firstStep + 1)
  await waitForAgentIdle(page)

  const source = await waitForActiveNativeSession(context, sourceId)
  expect(source.supportsFork, 'A supported provider must report supportsFork = true once a native session exists.').toBe(true)
  if (context.waitForNativeCheckpoint)
    await context.waitForNativeCheckpoint(source.agentSessionId)

  const { answerRows: originalAnswerRows, userSequence } = await readOriginalConversationRows(context, sourceId, texts)
  expect(await countChatBubbles(page, 'user', texts.originalPrompt, userSequence), 'The source tab draws the original prompt once.').toBe(1)
  const originalAnswerBubbles = await countChatBubbles(page, 'agent', texts.originalAnswer, userSequence)
  expect(originalAnswerBubbles, 'The source tab draws the original answer.').toBeGreaterThan(0)

  // Fork the idle source session into a new agent tab in the same workspace.
  const channel = await getTestChannel(leapmuxServer.hubUrl, leapmuxServer.adminToken)
  const forkedTabId = randomUUID()
  const forkResp = await channel.callWorker(
    leapmuxServer.workerId,
    WorkerAgentService.method.openAgent,
    {
      creation: {
        tabId: forkedTabId,
        placement: {
          workspaceId: context.workspaceId,
          destination: { case: 'firstLeaf', value: {} },
          insertion: TabInsertion.LAST,
        },
      },
      workingDir: source.workingDir,
      agentProvider: source.agentProvider,
      title: 'Forked',
      options: defaultForkOptions(source.agentProvider),
      forkSource: { sourceAgentId: source.id },
    },
  )
  expect(forkResp.agent?.id).toBe(forkedTabId)
  const userEvents = await getUserEventsSubscription(leapmuxServer.hubUrl, leapmuxServer.adminToken)
  await userEvents.awaitRootNodeId(context.workspaceId)

  const forked = await retryUntilPass(async () => {
    const agent = await nativeAgentById(context, forkedTabId)
    if (!agent)
      throw new Error(`The Worker holds no forked agent ${forkedTabId}.`)
    expect(agent.status, `Fork startup failed: ${agent.startupError}`).not.toBe(AgentStatus.STARTING)
    return agent
  })
  expect(forked.status, `Fork startup failed: ${forked.startupError}`).toBe(AgentStatus.ACTIVE)
  expect(forked.agentProvider).toBe(source.agentProvider)
  expect(forked.forkedFromAgentId).toBe(source.id)
  expect(forked.supportsFork).toBe(true)
  expect(forked.agentSessionId, 'The forked agent must confirm a native session ID.').not.toBe('')
  expect(forked.agentSessionId, 'The forked agent must receive its own native session ID distinct from the source.').not.toBe(source.agentSessionId)

  // Switch to the forked tab and verify the cloned conversation renders before sending turn 2.
  const forkedTab = tabById(page, forked.id)
  await expect(forkedTab).toBeVisible()
  await forkedTab.click()
  expect(await countChatBubbles(page, 'user', texts.originalPrompt, userSequence)).toBe(1)
  expect(await countChatBubbles(page, 'agent', texts.originalAnswer, userSequence)).toBe(originalAnswerBubbles)

  // Turn 2 on the forked agent: must carry prior context in order.
  const forkedStep = await modelScript.queue(nativeTextStep(context, texts.resumedAnswer))
  await sendMessage(page, modelScript.prompt(texts.resumedPrompt))
  await modelScript.waitForSteps(forkedStep + 1)
  await waitForAgentIdle(page)

  const forkedRequest = await modelScript.requestAt(forkedStep)
  expect(JSON.stringify(forkedRequest.body)).toContain(texts.originalAnswer)
  const readTurns = context.readConversationTurns ?? nativeModelConversationTurns
  expectNativeResumeContext(readTurns(forkedRequest), texts)
  expect(await countChatBubbles(page, 'agent', texts.resumedAnswer, userSequence)).toBe(originalAnswerBubbles)

  // Verify the forked transcript kept the cloned source rows without duplicating or merging them.
  await expectUnmergedAnswerBubbles(page, texts)
  const forkedSnapshot = await readNativeMessageSnapshot(context, forked.id)
  const clonedUsers = nativeMessagesHoldingText(forkedSnapshot.messages, texts.originalPrompt)
    .filter(message => message.source === MessageSource.USER)
  expect(clonedUsers, 'The forked transcript must hold exactly one cloned original prompt row.').toHaveLength(1)
  expect(isStoredMessageSeq(clonedUsers[0]!.seq)).toBe(true)
  expect(['', source.agentSessionId, forked.agentSessionId]).toContain(clonedUsers[0]!.agentSessionId)
  const clonedAnswers = nativeMessagesHoldingText(forkedSnapshot.messages, texts.originalAnswer)
  expect(clonedAnswers.length, 'Forking must not duplicate original answer rows.').toBe(originalAnswerRows)
  const forkedAnswerRows = nativeMessagesHoldingText(forkedSnapshot.messages, texts.resumedAnswer)
  expect(forkedAnswerRows.length, 'The forked agent must store a Worker row for the forked turn answer.').toBeGreaterThan(0)
  expect(nativeMessagesHoldingText(forkedAnswerRows, texts.originalAnswer).map(row => row.id)).toEqual([])
  expect(await countChatBubbles(page, 'user', texts.originalPrompt, clonedUsers[0]!.seq)).toBe(1)
  expect(await countChatBubbles(page, 'agent', texts.originalAnswer, clonedUsers[0]!.seq)).toBe(originalAnswerBubbles)

  // Turn 3 on the original source agent: must remain isolated from turn 2 on the fork.
  const sourceTab = tabById(page, source.id)
  await sourceTab.click()
  const sourceStep = await modelScript.queue(nativeTextStep(context, sourceFollowUpAnswer))
  await sendMessage(page, modelScript.prompt(sourceFollowUpPrompt))
  await modelScript.waitForSteps(sourceStep + 1)
  await waitForAgentIdle(page)

  const sourceFollowUpRequest = await modelScript.requestAt(sourceStep)
  const sourceFollowUpBody = JSON.stringify(sourceFollowUpRequest.body)
  expectNativeResumeContext(readTurns(sourceFollowUpRequest), {
    originalPrompt: texts.originalPrompt,
    originalAnswer: texts.originalAnswer,
    resumedPrompt: sourceFollowUpPrompt,
  })
  expect(sourceFollowUpBody, 'The original source session must not contain the forked branch prompt.').not.toContain(texts.resumedPrompt)
  expect(sourceFollowUpBody, 'The original source session must not contain the forked branch answer.').not.toContain(texts.resumedAnswer)
}

/**
 * Prove that a provider without LeapMux native session-fork support reports `supportsFork = false`
 * after a real native turn and refuses `OpenAgent` with `forkSource` cleanly without creating a tab.
 */
export async function expectUnsupportedFork(context: ManagedNativeScenarioContext): Promise<void> {
  const { page, modelScript, leapmuxServer } = context
  const marker = uniqueMarker()
  const prompt = `Keep FORKREFUSAL${marker} for the active session.`
  const answer = `FORKREFUSALANSWER${marker}`

  const initialAgent = await currentNativeAgent(context)
  const sourceId = initialAgent.id

  const firstStep = await modelScript.queue(nativeTextStep(context, answer))
  await sendMessage(page, modelScript.prompt(prompt))
  await modelScript.waitForSteps(firstStep + 1)
  await waitForAgentIdle(page)

  const source = await waitForActiveNativeSession(context, sourceId)
  expect(source.supportsFork, 'An unsupported provider must report supportsFork = false.').toBe(false)

  const channel = await getTestChannel(leapmuxServer.hubUrl, leapmuxServer.adminToken)
  const attemptedTabId = randomUUID()
  await expect(channel.callWorker(
    leapmuxServer.workerId,
    WorkerAgentService.method.openAgent,
    {
      creation: {
        tabId: attemptedTabId,
        placement: {
          workspaceId: context.workspaceId,
          destination: { case: 'firstLeaf', value: {} },
          insertion: TabInsertion.LAST,
        },
      },
      workingDir: source.workingDir,
      agentProvider: source.agentProvider,
      title: 'Refused Fork',
      options: defaultForkOptions(source.agentProvider),
      forkSource: { sourceAgentId: source.id },
    },
  )).rejects.toMatchObject({
    source: 'rpc',
    code: Code.FailedPrecondition,
    message: expect.stringMatching(/does not support native conversation-history forking/),
  })

  expect(await nativeAgentById(context, attemptedTabId), 'A refused fork must not leave an orphan agent row.').toBeNull()
  const after = await nativeAgentById(context, sourceId)
  expect(after?.status, 'A refused fork must leave the source agent active.').toBe(AgentStatus.ACTIVE)
}
