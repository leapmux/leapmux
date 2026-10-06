import type { Page } from '@playwright/test'
import type { ServerInfo } from '../fixtures'
import type { MockModelRequestRecord } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { NativeResumeResult } from './nativeLifecycle'
import type { StoredSessionList } from './nativeResume'
import type { ManagedNativeScenarioContext, NativeContextFixtures } from './nativeScenario'
import { expect } from '@playwright/test'
import { agentOpenOptions } from '../agentSettings'
import { createWorkspaceViaAPI, openAgentViaAPI } from './api'
import { stepRequest } from './mockModelScript'
import { countOriginalAnswerRows, expectNativeResumeContext, expectReopenedNativeAgent, expectResumedConversation, nativeResumeTexts, reopenFromSessionPicker } from './nativeResume'
import { nativeAgentById, nativeModelConversationTurns, nativeTextStep } from './nativeScenario'
import { agentTabs, assistantBubbles, loginViaToken, openWorkspace, sendMessage, userBubbles, waitForAgentIdle } from './ui'
import { closeAgentViaAPI, createGitRepo } from './worktree'

/** The fixtures one picker resume spec already holds: the shared page, the scripted model and the running hub. */
export interface ResumePickerFixtures {
  readonly page: Page
  readonly modelScript: ModelScript
  readonly leapmuxServer: Pick<ServerInfo, 'hubUrl' | 'adminToken' | 'workerId' | 'dataDir'>
}

/** The `nativeContext` of a provider directory. It states every fact of the provider that the scenario needs. */
export type ResumePickerNativeContext = (fixtures: NativeContextFixtures) => Promise<ManagedNativeScenarioContext>

/** The parts of the picker resume scenario that the provider context does not hold. Everything else is one shared flow. */
export interface ResumePickerOptions {
  /** The label names the workspace and nothing else. */
  readonly label: string
  /** How the session menu lists the stored session. `by-id` by default. */
  readonly sessionList?: StoredSessionList
  /** Option values for the subject agent, over the provider defaults the scenario already applies. */
  readonly subjectOptionValues?: Record<string, string>
  /**
   * Also assert the original turn's one prompt and answer bubble right after it ends, and exactly one resumed
   * answer bubble after the continued turn. Absent by default: the Worker-row proofs in `expectResumedConversation`
   * already cover the reopened transcript.
   */
  readonly assertConversationBubbles?: boolean
  /** Inspect the request of the original turn right after its model answer, before the turn ends. */
  readonly onFirstTurn?: (request: MockModelRequestRecord) => void | Promise<void>
  /** Assert provider-specific facts about the request that consumed the resumed prompt. */
  readonly onResumedRequest?: (request: MockModelRequestRecord) => void | Promise<void>
  /**
   * Prove the original answer inside the resumed request body. Every provider that restates its history in the
   * request holds it there, so this is the default. A provider that sends only the current prompt and keeps the
   * conversation on its service (Cursor) passes `false`, and its context reader of conversation turns proves the
   * context.
   */
  readonly resumedBodyHoldsOriginalAnswer?: boolean
}

/**
 * Run the whole stored-session picker resume scenario for one provider, and prove these conditions:
 *
 * - The stored session of a closed subject agent reopens through the New Agent dialog's session picker, and the
 *   Worker confirms the stored provider and native session for the reopened agent.
 * - The continued turn reached the native model holding the original exchange in order, through the conversation
 *   reader of the provider context.
 * - The reopened transcript draws exactly the Worker rows the original agent stored, and the resumed answer stays
 *   a separate row and bubble.
 *
 * The scenario opens its own workspace, and builds the provider context for it through `nativeContext`. So the
 * answer step and the conversation reader of the provider come from that context. The spec keeps its fixtures, skip
 * handling and title. Provider-specific assertions beyond the shared flow run in `onFirstTurn` and
 * `onResumedRequest`.
 */
export async function resumePickerScenario(
  fixtures: ResumePickerFixtures,
  nativeContext: ResumePickerNativeContext,
  options: ResumePickerOptions,
): Promise<NativeResumeResult> {
  const { page, modelScript } = fixtures
  const { hubUrl, adminToken, workerId, dataDir } = fixtures.leapmuxServer
  const keeperDir = createGitRepo(dataDir, `resume-keeper-${crypto.randomUUID()}`)
  const subjectDir = createGitRepo(dataDir, `resume-subject-${crypto.randomUUID()}`)
  const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, `${options.label} resume ${crypto.randomUUID()}`)
  const context = await nativeContext({ page, modelScript, leapmuxServer: fixtures.leapmuxServer, workspaceId })
  const provider = context.provider
  const keeperId = await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, keeperDir, { title: 'Keeper' })
  const subjectId = await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, subjectDir, {
    ...agentOpenOptions(provider, options.subjectOptionValues ? { optionValues: options.subjectOptionValues } : {}),
    title: 'Subject',
  })
  await loginViaToken(page, adminToken)
  await openWorkspace(page, workspaceId)
  await agentTabs(page).filter({ hasText: 'Subject' }).first().click()
  const texts = nativeResumeTexts()
  const originalStep = await modelScript.queue(nativeTextStep(context, texts.originalAnswer))
  await sendMessage(page, modelScript.prompt(texts.originalPrompt))
  const firstStatus = await modelScript.waitForSteps(originalStep + 1)
  await options.onFirstTurn?.(stepRequest(firstStatus, originalStep))
  await waitForAgentIdle(page)
  if (options.assertConversationBubbles) {
    await expect(userBubbles(page).filter({ hasText: texts.originalPrompt })).toHaveCount(1)
    await expect(assistantBubbles(page).filter({ hasText: texts.originalAnswer })).toHaveCount(1)
  }
  let sessionId = ''
  await expect.poll(async () => {
    sessionId = (await nativeAgentById(context, subjectId))?.agentSessionId ?? ''
    return sessionId
  }).not.toBe('')
  const originalAnswerRows = await countOriginalAnswerRows(context, subjectId, texts)
  // What the live transcript drew before the close is what the reopened one must
  // draw again: an answer tool can store the answer in rows that render no
  // bubble, so the page count is its own number, not the row count.
  const originalAnswerBubbles = await assistantBubbles(page).filter({ hasText: texts.originalAnswer }).count()
  await closeAgentViaAPI(hubUrl, adminToken, workerId, subjectId)

  await reopenFromSessionPicker(page, { provider, workingDir: subjectDir, sessionId, ...(options.sessionList ? { list: options.sessionList } : {}) })
  const reopened = await expectReopenedNativeAgent(context, { agentProvider: provider, agentSessionId: sessionId }, [keeperId, subjectId])

  await expect(userBubbles(page).filter({ hasText: texts.originalPrompt })).toHaveCount(1)
  await expect(assistantBubbles(page).filter({ hasText: texts.originalAnswer })).toHaveCount(originalAnswerBubbles)
  const resumedStep = await modelScript.queue(nativeTextStep(context, texts.resumedAnswer))
  await sendMessage(page, modelScript.prompt(texts.resumedPrompt))
  await modelScript.waitForSteps(resumedStep + 1)
  await waitForAgentIdle(page)
  // Read the record after the turn. The mock counts a step when its request arrives, and a native client
  // states more of that request later, such as the rules of a context query.
  const resumed = await modelScript.requestAt(resumedStep)
  if (options.resumedBodyHoldsOriginalAnswer !== false)
    expect(JSON.stringify(resumed.body)).toContain(texts.originalAnswer)
  await options.onResumedRequest?.(resumed)
  expectNativeResumeContext((context.readConversationTurns ?? nativeModelConversationTurns)(resumed), texts)
  if (options.assertConversationBubbles)
    await expect(assistantBubbles(page).filter({ hasText: texts.resumedAnswer })).toHaveCount(1)
  await expectResumedConversation(context, reopened.id, texts, originalAnswerRows, originalAnswerBubbles)
  return { ...texts, request: resumed }
}
