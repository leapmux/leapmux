import type { Page } from '@playwright/test'
import type { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ServerInfo } from '../fixtures'
import type { MockModelRequestRecord, MockModelRule, MockModelScenarioStatus, MockModelStep } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { NativeResumeResult } from './nativeLifecycle'
import type { NativeResumeTexts } from './nativeResume'
import type { NativeModelTurn } from './nativeScenario'
import { expect } from '@playwright/test'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { createWorkspaceViaAPI, openAgentViaAPI } from './api'
import { countOriginalAnswerRows, expectNativeResumeContext, expectReopenedNativeAgent, expectResumedConversation, nativeResumeTexts } from './nativeResume'
import { nativeModelConversationTurns } from './nativeScenario'
import { listAgents } from './subagentRegistry'
import { assistantBubbles, loginViaToken, openMenu, openWorkspace, sendMessage, userBubbles, waitForAgentIdle } from './ui'
import { closeAgentViaAPI, createGitRepo, openNewAgentDialog, setWorkingDir, waitForWorker } from './worktree'

/** The fixtures one picker resume spec already holds: the shared page, the scripted model and the running hub. */
export interface ResumePickerContext {
  readonly page: Page
  readonly modelScript: ModelScript
  readonly leapmuxServer: Pick<ServerInfo, 'hubUrl' | 'adminToken' | 'workerId' | 'dataDir'>
}

/** How the picker states the stored session of the subject's working directory. */
export type ResumePickerSessionList
  = | 'stored-option'
    | 'newest-of-three'

/** The provider-owned parts of the picker resume scenario. Everything else is one shared flow. */
export interface ResumePickerOptions {
  readonly provider: AgentProvider
  /** The label names the workspace and nothing else. */
  readonly label: string
  /** Rules that answer the provider's own housekeeping turns, so they never take a scripted answer. */
  readonly rules?: readonly MockModelRule[]
  /** Build the scripted answer of one turn. A plain text step by default; a provider whose final answer is a tool call passes its own. */
  readonly answerStep?: (texts: NativeResumeTexts, turn: 'original' | 'resumed') => MockModelStep
  /**
   * How the session menu states the stored session. `stored-option` (the default) finds it by its own test id.
   * `newest-of-three` is for a CLI that seeds extra sessions into the working directory: the menu holds exactly
   * three options and the stored session is the newest, the third.
   */
  readonly sessionList?: ResumePickerSessionList
  /** Read the conversation turns of the resumed request. The generic model reader by default. */
  readonly conversationTurns?: (request: MockModelRequestRecord) => NativeModelTurn[]
  /** Option values for the subject agent, over the provider defaults the scenario already applies. */
  readonly subjectOptionValues?: Record<string, string>
  /**
   * Also assert the original turn's one prompt and answer bubble right after it ends, and exactly one resumed
   * answer bubble after the continued turn. Absent by default: the Worker-row proofs in `expectResumedConversation`
   * already cover the reopened transcript.
   */
  readonly assertConversationBubbles?: boolean
  /** The `waitForAgentIdle` timeout of both turns. The helper default is `waitForAgentIdle`'s own. */
  readonly idleTimeoutMs?: number
  /** Inspect the recorded requests of the original turn right after its model answer, before the turn ends. */
  readonly onFirstTurn?: (status: MockModelScenarioStatus) => void | Promise<void>
  /** Assert provider-specific facts about the request that consumed the resumed prompt. */
  readonly onResumedRequest?: (request: MockModelRequestRecord) => void | Promise<void>
  /**
   * Prove the original answer inside the resumed request body. Every provider that restates its history in the
   * request holds it there, so this is the default. A provider that sends only the current prompt and keeps the
   * conversation on its service (Cursor) passes `false` and proves the context through `conversationTurns`.
   */
  readonly resumedBodyHoldsOriginalAnswer?: boolean
}

/**
 * Run the whole stored-session picker resume scenario for one provider, and prove these conditions:
 *
 * - The stored session of a closed subject agent reopens through the New Agent dialog's session picker, and the
 *   Worker confirms the stored provider and native session for the reopened agent.
 * - The continued turn reached the native model holding the original exchange in order, through the scenario's
 *   turn reader.
 * - The reopened transcript draws exactly the Worker rows the original agent stored, and the resumed answer stays
 *   a separate row and bubble.
 *
 * The spec keeps its fixtures, skip handling and title, and states only the provider-owned parts through
 * `options`. Provider-specific assertions beyond the shared flow run in `onFirstTurn` and `onResumedRequest`.
 */
export async function resumePickerScenario(
  context: ResumePickerContext,
  options: ResumePickerOptions,
): Promise<NativeResumeResult> {
  const { page, modelScript } = context
  const { hubUrl, adminToken, workerId, dataDir } = context.leapmuxServer
  const provider = options.provider
  const keeperDir = createGitRepo(dataDir, `resume-keeper-${crypto.randomUUID()}`)
  const subjectDir = createGitRepo(dataDir, `resume-subject-${crypto.randomUUID()}`)
  const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, `${options.label} resume ${crypto.randomUUID()}`)
  const keeperId = await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, keeperDir, { title: 'Keeper' })
  const initialSettings = agentOpenOptions(agentSettings(provider))
  const subjectId = await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, subjectDir, {
    agentProvider: provider,
    ...initialSettings,
    ...(options.subjectOptionValues ? { optionValues: { ...initialSettings.optionValues, ...options.subjectOptionValues } } : {}),
    title: 'Subject',
  })
  await loginViaToken(page, adminToken)
  await openWorkspace(page, workspaceId)
  await page.locator('[data-testid="tab"][data-tab-type="agent"]').filter({ hasText: 'Subject' }).first().click()
  if (options.rules && options.rules.length > 0)
    await modelScript.rule(...options.rules)
  const texts = nativeResumeTexts()
  const answerStep = (turn: 'original' | 'resumed'): MockModelStep =>
    options.answerStep?.(texts, turn) ?? { text: turn === 'original' ? texts.originalAnswer : texts.resumedAnswer }
  await modelScript.queue(answerStep('original'))
  await sendMessage(page, modelScript.prompt(texts.originalPrompt))
  const firstStatus = await modelScript.waitForSteps(1)
  await options.onFirstTurn?.(firstStatus)
  await waitForAgentIdle(page, options.idleTimeoutMs)
  if (options.assertConversationBubbles) {
    await expect(userBubbles(page).filter({ hasText: texts.originalPrompt })).toHaveCount(1)
    await expect(assistantBubbles(page).filter({ hasText: texts.originalAnswer })).toHaveCount(1)
  }
  let sessionId = ''
  await expect.poll(async () => {
    const agents = await listAgents(hubUrl, adminToken, workerId, [subjectId])
    sessionId = agents?.find(agent => agent.id === subjectId)?.agentSessionId ?? ''
    return sessionId
  }).not.toBe('')
  const originalAnswerRows = await countOriginalAnswerRows({ leapmuxServer: context.leapmuxServer }, subjectId, texts)
  // What the live transcript drew before the close is what the reopened one must
  // draw again: an answer tool can store the answer in rows that render no
  // bubble, so the page count is its own number, not the row count.
  const originalAnswerBubbles = await assistantBubbles(page).filter({ hasText: texts.originalAnswer }).count()
  await closeAgentViaAPI(hubUrl, adminToken, workerId, subjectId)

  await openNewAgentDialog(page)
  await waitForWorker(page)
  const dialog = page.getByRole('dialog')
  await dialog.getByTestId('agent-provider-selector-trigger').click()
  await page.getByTestId(`agent-provider-option-${provider}`).click()
  await setWorkingDir(page, subjectDir)
  const menu = dialog.getByTestId('session-select-menu')
  await expect(dialog.getByTestId('session-select-menu-trigger')).toBeEnabled()
  await openMenu(dialog, 'session-select-menu')
  let session = menu.getByTestId(`loading-menu-option-${sessionId}`)
  if (options.sessionList === 'newest-of-three') {
    const rows = menu.getByRole('menuitemradio')
    await expect(rows).toHaveCount(3)
    session = rows.nth(2)
    await expect(session).toHaveAttribute('data-testid', `loading-menu-option-${sessionId}`)
  }
  else {
    await expect(session).toBeVisible()
  }
  await session.click()
  await dialog.getByRole('button', { name: 'Create' }).click()
  const reopened = await expectReopenedNativeAgent({ page, leapmuxServer: context.leapmuxServer }, { agentProvider: provider, agentSessionId: sessionId }, [keeperId, subjectId])

  await expect(userBubbles(page).filter({ hasText: texts.originalPrompt })).toHaveCount(1)
  await expect(assistantBubbles(page).filter({ hasText: texts.originalAnswer })).toHaveCount(originalAnswerBubbles)
  await modelScript.queue(answerStep('resumed'))
  await sendMessage(page, modelScript.prompt(texts.resumedPrompt))
  await modelScript.waitForSteps(2)
  await waitForAgentIdle(page, options.idleTimeoutMs)
  // Read the record after the turn. The mock counts a step when its request arrives, and a native client
  // states more of that request later, such as the rules of a context query.
  const resumed = (await modelScript.status()).requests.find(request => request.stepIndex === 1)
  if (!resumed)
    throw new Error('The resumed prompt reached no native model request.')
  if (options.resumedBodyHoldsOriginalAnswer !== false)
    expect(JSON.stringify(resumed.body)).toContain(texts.originalAnswer)
  await options.onResumedRequest?.(resumed)
  expectNativeResumeContext((options.conversationTurns ?? nativeModelConversationTurns)(resumed), texts)
  if (options.assertConversationBubbles)
    await expect(assistantBubbles(page).filter({ hasText: texts.resumedAnswer })).toHaveCount(1)
  await expectResumedConversation({ page, leapmuxServer: context.leapmuxServer }, reopened.id, texts, originalAnswerRows, originalAnswerBubbles)
  return { ...texts, request: resumed }
}
