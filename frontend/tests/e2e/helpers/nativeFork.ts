import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { randomUUID } from 'node:crypto'
import { Code } from '@connectrpc/connect'
import { expect } from '@playwright/test'
import { AgentStatus, MessageSource } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { openAgentViaAPI } from './api'
import { countChatBubbles } from './chatBubbleCount'
import { isStoredMessageSeq, nativeMessagesHoldingText, readAllAgentMessages, readNativeMessageSnapshot } from './nativeMessages'
import { expectNativeResumeContext, expectUnmergedAnswerBubbles, nativeResumeTexts, readOriginalConversationRows } from './nativeResume'
import { currentNativeAgent, nativeAgentById, nativeModelConversationTurns, nativeTextStep } from './nativeScenario'
import { deliberateWorkingDir } from './providerWorkingDir'
import { retryUntilPass } from './retryUntilPass'
import { uniqueMarker } from './shellArguments'
import { assistantBubbles, sendMessage, tabById, userBubbles, waitForAgentIdle } from './ui'

/** Queue one scripted assistant answer, send the user prompt, and wait for the turn to settle. */
async function sendScriptedTurn(
  context: ManagedNativeScenarioContext,
  prompt: string,
  answer: string,
): Promise<number> {
  const { page, modelScript } = context
  const step = await modelScript.queue(nativeTextStep(context, answer))
  await sendMessage(page, modelScript.prompt(prompt))
  await modelScript.waitForSteps(step + 1)
  await waitForAgentIdle(page)
  return step
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
  await sendScriptedTurn(context, texts.originalPrompt, texts.originalAnswer)

  const source = await waitForActiveNativeSession(context, sourceId)
  expect(source.supportsFork, 'A supported provider must report supportsFork = true once a native session exists.').toBe(true)
  if (context.waitForNativeCheckpoint)
    await context.waitForNativeCheckpoint(source.agentSessionId)

  const { answerRows: originalAnswerRows, userSequence } = await readOriginalConversationRows(context, sourceId, texts)
  expect(await countChatBubbles(page, 'user', texts.originalPrompt, userSequence), 'The source tab draws the original prompt once.').toBe(1)
  const originalAnswerBubbles = await countChatBubbles(page, 'agent', texts.originalAnswer, userSequence)
  expect(originalAnswerBubbles, 'The source tab draws the original answer.').toBeGreaterThan(0)

  // Fork the idle source session into a new agent tab in the same workspace.
  const forkedTabId = randomUUID()
  const openedForkId = await openAgentViaAPI(
    leapmuxServer,
    context.workspaceId,
    deliberateWorkingDir(source.workingDir, 'Fork opens in the directory where the Worker holds the source session.'),
    {
      ...agentOpenOptions(source.agentProvider),
      tabId: forkedTabId,
      title: 'Forked',
      forkSource: { sourceAgentId: source.id },
    },
  )
  expect(openedForkId).toBe(forkedTabId)

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
  if (forked.agentSessionId !== '') {
    expect(forked.supportsFork).toBe(true)
    expect(forked.agentSessionId, 'The forked agent must receive its own native session ID distinct from the source.').not.toBe(source.agentSessionId)
  }

  const clonedBeforeTurn2 = await retryUntilPass(async () => {
    // Providers like Claude Code defer native session materialization until turn 1,
    // so read cloned Worker rows directly without requiring a non-empty agentSessionId yet.
    const messages = await readAllAgentMessages(context, forked.id)
    const clonedUsers = nativeMessagesHoldingText(messages, texts.originalPrompt)
      .filter(message => message.source === MessageSource.USER)
    if (clonedUsers.length !== 1 || !isStoredMessageSeq(clonedUsers[0]!.seq)) {
      throw new Error(`Expected exactly one cloned original prompt row with positive sequence, got ${clonedUsers.length}.`)
    }
    const allowedSessionIDs = ['', source.agentSessionId, forked.agentSessionId]
    if (!allowedSessionIDs.includes(clonedUsers[0]!.agentSessionId)) {
      throw new Error(`Unexpected cloned user row session ID ${clonedUsers[0]!.agentSessionId}.`)
    }
    return { userSequence: clonedUsers[0]!.seq }
  })

  // Switch to the forked tab and wait for tab activation and cloned transcript hydration before sending turn 2.
  const forkedTab = tabById(page, forked.id)
  await expect(forkedTab).toBeVisible()
  await forkedTab.click()
  await expect(forkedTab).toHaveAttribute('aria-selected', 'true')
  await expect(userBubbles(page).filter({ hasText: texts.originalPrompt }).first()).toBeVisible()
  await expect(assistantBubbles(page).filter({ hasText: texts.originalAnswer }).first()).toBeVisible()
  await expect.poll(
    () => countChatBubbles(page, 'user', texts.originalPrompt, clonedBeforeTurn2.userSequence),
    { message: 'The forked tab draws the cloned original prompt once.' },
  ).toBe(1)
  await expect.poll(
    () => countChatBubbles(page, 'agent', texts.originalAnswer, clonedBeforeTurn2.userSequence),
    { message: 'The forked tab draws the cloned original answer.' },
  ).toBe(originalAnswerBubbles)

  // Turn 2 on the forked agent: must carry prior context in order.
  const forkedStep = await sendScriptedTurn(context, texts.resumedPrompt, texts.resumedAnswer)

  const forkedRequest = await modelScript.requestAt(forkedStep)
  expect(JSON.stringify(forkedRequest.body)).toContain(texts.originalAnswer)
  const readTurns = context.readConversationTurns ?? nativeModelConversationTurns
  expectNativeResumeContext(readTurns(forkedRequest), texts)

  // Verify the forked agent confirmed its own distinct native session ID (providers like Claude Code materialize it on turn 1).
  const forkedAfterTurn2 = await retryUntilPass(async () => {
    const agent = await nativeAgentById(context, forked.id)
    if (!agent || agent.agentSessionId === '')
      throw new Error(`The forked agent ${forked.id} has not confirmed a native session ID yet.`)
    return agent
  })
  expect(forkedAfterTurn2.supportsFork).toBe(true)
  expect(forkedAfterTurn2.agentSessionId, 'The forked agent must receive its own native session ID distinct from the source.').not.toBe(source.agentSessionId)

  // Verify the forked transcript kept the cloned source rows without duplicating or merging them.
  await expectUnmergedAnswerBubbles(page, texts)
  const forkedSnapshot = await readNativeMessageSnapshot(context, forked.id)
  const clonedUsers = nativeMessagesHoldingText(forkedSnapshot.messages, texts.originalPrompt)
    .filter(message => message.source === MessageSource.USER)
  expect(clonedUsers, 'The forked transcript must hold exactly one cloned original prompt row.').toHaveLength(1)
  expect(isStoredMessageSeq(clonedUsers[0]!.seq)).toBe(true)
  expect(['', source.agentSessionId, forkedAfterTurn2.agentSessionId]).toContain(clonedUsers[0]!.agentSessionId)
  const clonedAnswers = nativeMessagesHoldingText(forkedSnapshot.messages, texts.originalAnswer)
  expect(clonedAnswers.length, 'Forking must not duplicate original answer rows.').toBe(originalAnswerRows)
  const forkedAnswerRows = nativeMessagesHoldingText(forkedSnapshot.messages, texts.resumedAnswer)
  expect(forkedAnswerRows.length, 'The forked agent must store a Worker row for the forked turn answer.').toBeGreaterThan(0)
  expect(nativeMessagesHoldingText(forkedAnswerRows, texts.originalAnswer).map(row => row.id)).toEqual([])
  const forkedTurnSequence = forkedAnswerRows[0]!.seq
  expect(isStoredMessageSeq(forkedTurnSequence)).toBe(true)
  await expect.poll(
    () => countChatBubbles(page, 'agent', texts.resumedAnswer, forkedTurnSequence),
    { message: 'The forked tab draws the turn 2 answer.' },
  ).toBeGreaterThanOrEqual(1)
  await expect.poll(
    () => countChatBubbles(page, 'user', texts.originalPrompt, clonedUsers[0]!.seq),
    { message: 'The forked tab keeps the cloned original prompt once.' },
  ).toBe(1)
  await expect.poll(
    () => countChatBubbles(page, 'agent', texts.originalAnswer, clonedUsers[0]!.seq),
    { message: 'The forked tab keeps the cloned original answer.' },
  ).toBe(originalAnswerBubbles)

  // Turn 3 on the original source agent: must remain isolated from turn 2 on the fork.
  const sourceTab = tabById(page, source.id)
  await expect(sourceTab).toBeVisible()
  await sourceTab.click()
  await expect(sourceTab).toHaveAttribute('aria-selected', 'true')
  await expect(userBubbles(page).filter({ hasText: texts.originalPrompt }).first()).toBeVisible()
  await expect(assistantBubbles(page).filter({ hasText: texts.originalAnswer }).first()).toBeVisible()
  await expect(assistantBubbles(page).filter({ hasText: texts.resumedAnswer })).toHaveCount(0)
  const sourceStep = await sendScriptedTurn(context, sourceFollowUpPrompt, sourceFollowUpAnswer)

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
  const { leapmuxServer } = context
  const marker = uniqueMarker()
  const prompt = `Keep FORKREFUSAL${marker} for the active session.`
  const answer = `FORKREFUSALANSWER${marker}`

  const initialAgent = await currentNativeAgent(context)
  const sourceId = initialAgent.id

  await sendScriptedTurn(context, prompt, answer)

  const source = await waitForActiveNativeSession(context, sourceId)
  expect(source.supportsFork, 'An unsupported provider must report supportsFork = false.').toBe(false)

  const attemptedTabId = randomUUID()
  await expect(openAgentViaAPI(
    leapmuxServer,
    context.workspaceId,
    deliberateWorkingDir(source.workingDir, 'Fork refusal targets the directory where the Worker holds the source session.'),
    {
      ...agentOpenOptions(source.agentProvider),
      tabId: attemptedTabId,
      title: 'Refused Fork',
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
