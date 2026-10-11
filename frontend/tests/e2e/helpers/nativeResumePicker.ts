import type { Page } from '@playwright/test'
import type { AgentOpenOptions } from '../agentSettings'
import type { ServerInfo } from '../fixtures'
import type { MockModelRequestRecord } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { NativeResumeResult } from './nativeLifecycle'
import type { StoredSessionList } from './nativeResume'
import type { ManagedNativeScenarioContext, NativeContextFixtures } from './nativeScenario'
import type { ProviderWorkingDir } from './providerWorkingDir'
import { expect } from '@playwright/test'
import { agentOpenOptions } from '../agentSettings'
import { createWorkspaceViaAPI, openAgentViaAPI } from './api'
import { stepRequest } from './mockModelScript'
import { countOriginalAnswerRows, expectNativeResumeContext, expectReopenedNativeAgent, expectResumedConversation, nativeResumeTexts, reopenFromSessionPicker } from './nativeResume'
import { nativeAgentById, nativeModelConversationTurns, nativeTextStep } from './nativeScenario'
import { deliberateWorkingDir } from './providerWorkingDir'
import { retryUntilPass } from './retryUntilPass'
import { agentTabs, assistantBubbles, loginViaToken, openWorkspace, sendMessage, userBubbles, waitForAgentIdle } from './ui'
import { closeNativeAgentAndWait } from './workerTabs'
import { createGitRepo } from './worktree'

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
 * Create a git repository of its own for one directory of a session picker flow, as `<dataDir>/<prefix><UUID>`.
 *
 * The rule of a provider cannot make this layout: it makes a plain directory of the run for most providers. A provider
 * can list the sessions of the whole git repository around a directory, or of the nearest directory that it takes as
 * the project: Amp keeps the threads of the git top level of the directory. So each directory of a picker flow is the
 * root of a repository of its own, and the picker of one directory lists only the sessions of that directory.
 */
export function sessionPickerRepository(dataDir: string, prefix: string): ProviderWorkingDir {
  return deliberateWorkingDir(
    createGitRepo(dataDir, `${prefix}${crypto.randomUUID()}`),
    'The session picker of one directory must list only the sessions of that directory, so the directory is the root of a git repository of its own.',
  )
}

/** The two agents of a picker resume flow, and the directory of the subject. */
export interface ResumeSubject {
  workspaceId: string
  keeperId: string
  subjectId: string
  /** The repository of the subject, where the session picker finds its stored session (`sessionPickerRepository`). */
  subjectDir: ProviderWorkingDir
}

/**
 * Open the two agents of a picker resume flow in a new workspace, sign the page in, and select the subject:
 *
 * - The keeper keeps the workspace open after the subject closes. It opens with the Worker default.
 * - The subject is the agent whose stored session the flow reopens. It opens with the options that `subjectOptions`
 *   returns, or with the Worker default when the flow states none. The callback runs after the workspace exists and
 *   before either agent opens, so a flow can build a context for the workspace there.
 *
 * Each agent works in a git repository of its own (`sessionPickerRepository`), so the picker of the subject directory
 * lists the subject's sessions alone. The flow selects the subject by its tab, because the tab that the app selects on
 * load is not the contract of the flow.
 */
export async function openResumeSubject(
  fixtures: ResumePickerFixtures,
  options: { label: string, subjectOptions?: (workspaceId: string) => AgentOpenOptions | Promise<AgentOpenOptions> },
): Promise<ResumeSubject> {
  const { page, leapmuxServer: server } = fixtures
  const keeperDir = sessionPickerRepository(server.dataDir, 'resume-keeper-')
  const subjectDir = sessionPickerRepository(server.dataDir, 'resume-subject-')
  const workspaceId = await createWorkspaceViaAPI(server.hubUrl, server.adminToken, `${options.label} resume ${crypto.randomUUID()}`)
  const subjectOptions = await options.subjectOptions?.(workspaceId)
  const keeperId = await openAgentViaAPI(server, workspaceId, keeperDir, { title: 'Keeper' })
  const subjectId = await openAgentViaAPI(server, workspaceId, subjectDir, { ...subjectOptions, title: 'Subject' })
  await loginViaToken(page, server.adminToken)
  await openWorkspace(page, workspaceId)
  await agentTabs(page).filter({ hasText: 'Subject' }).first().click()
  return { workspaceId, keeperId, subjectId, subjectDir }
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
  // The provider context needs the workspace, and the subject opens with the defaults of its provider.
  let built: ManagedNativeScenarioContext | undefined
  const { keeperId, subjectId, subjectDir } = await openResumeSubject(fixtures, {
    label: options.label,
    subjectOptions: async (workspaceId) => {
      built = await nativeContext({ page, modelScript, leapmuxServer: fixtures.leapmuxServer, workspaceId })
      return agentOpenOptions(built.provider, options.subjectOptionValues ? { optionValues: options.subjectOptionValues } : {})
    },
  })
  if (!built)
    throw new Error('The picker resume scenario built no provider context.')
  const context = built
  const provider = context.provider
  const texts = nativeResumeTexts()
  const originalStep = await modelScript.queue(nativeTextStep(context, texts.originalAnswer))
  await sendMessage(page, modelScript.prompt(texts.originalPrompt))
  const firstStatus = await modelScript.waitForSteps(originalStep + 1)
  await options.onFirstTurn?.(stepRequest(firstStatus, originalStep))
  await waitForAgentIdle(page)
  await expect(userBubbles(page).filter({ hasText: texts.originalPrompt }), 'The live transcript draws the original prompt once.').toHaveCount(1)
  await expect(assistantBubbles(page).filter({ hasText: texts.originalAnswer }).first(), 'The live transcript draws the original answer.').toBeVisible()
  // Read the record after the turn, for the same reason as the resumed request below.
  const originalRequest = await modelScript.requestAt(originalStep)
  const sessionId = await retryUntilPass(async () => {
    const stored = (await nativeAgentById(context, subjectId))?.agentSessionId ?? ''
    expect(stored, 'the Worker stores the native session of the first turn').not.toBe('')
    return stored
  })
  const originalAnswerRows = await countOriginalAnswerRows(context, subjectId, texts)
  // What the live transcript drew before the close is what the reopened one must
  // draw again, and what the live resumed answer must draw: an answer tool can
  // store the answer in rows that render no bubble, or in several rows that each
  // render one, so the page count is its own number, not the row count.
  const originalAnswerBubbles = await assistantBubbles(page).filter({ hasText: texts.originalAnswer }).count()
  await closeNativeAgentAndWait(context, subjectId)

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
  // The resumed answer comes from the same answer step as the original one, so it draws the same number of bubbles.
  await expect(assistantBubbles(page).filter({ hasText: texts.resumedAnswer }), 'The resumed answer draws as many bubbles as the original answer.').toHaveCount(originalAnswerBubbles)
  await expectResumedConversation(context, reopened.id, texts, originalAnswerRows, originalAnswerBubbles)
  return { ...texts, originalRequest, request: resumed }
}
