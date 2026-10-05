import type { Page } from '@playwright/test'
import type { AgentChatMessage, AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ManagedNativeScenarioContext, NativeModelTurn } from './nativeScenario'
import { randomUUID } from 'node:crypto'
import { expect } from '@playwright/test'
import { AgentStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeMessagesHoldingText, readNativeMessageSnapshot } from './nativeMessages'
import { nativeAgentById, selectedAgentTab } from './nativeScenario'
import { assistantBubbles, userBubbles } from './ui'

const RESUME_MARKER = /^[a-f0-9]{32}$/

/** The texts of the original turn and of the turn that continues it in the resumed agent. */
export interface NativeResumeTexts {
  /** The 32-digit lowercase hexadecimal marker that each text holds. */
  readonly marker: string
  readonly originalPrompt: string
  readonly originalAnswer: string
  readonly resumedPrompt: string
  readonly resumedAnswer: string
}

/** The identity that the Worker must confirm for the reopened agent. */
export type NativeResumeIdentity = Pick<AgentInfo, 'agentProvider' | 'agentSessionId'>

/** The texts that a native model request must hold in turn order after a resume. */
export type NativeResumeContextTexts = Pick<NativeResumeTexts, 'originalPrompt' | 'originalAnswer' | 'resumedPrompt'>

/** The index of each text in the native turns. An absent text has the index -1. */
export interface NativeResumeContextOrder {
  readonly originalPrompt: number
  readonly originalAnswer: number
  readonly resumedPrompt: number
}

/** Build the texts of one resume scenario. No text contains another, so a search for one text never finds another. */
export function nativeResumeTexts(marker: string = randomUUID().replaceAll('-', '')): NativeResumeTexts {
  if (!RESUME_MARKER.test(marker))
    throw new Error('The native resume marker must be 32 lowercase hexadecimal digits.')
  return {
    marker,
    originalPrompt: `Keep RESUMEPROMPT${marker} for the stored session.`,
    originalAnswer: `RESUMEANSWER${marker}`,
    resumedPrompt: `Reply to RESUMEDPROMPT${marker} in the reopened session.`,
    resumedAnswer: `RESUMEDNEWANSWER${marker}`,
  }
}

/** Find the first user turn of each prompt and the first assistant turn of the original answer. */
export function nativeResumeContextOrder(turns: readonly NativeModelTurn[], texts: NativeResumeContextTexts): NativeResumeContextOrder {
  const find = (role: NativeModelTurn['role'], text: string) => {
    if (text.trim() === '')
      throw new Error('The native resume context search requires nonempty text.')
    return turns.findIndex(turn => turn.role === role && turn.text.includes(text))
  }
  return {
    originalPrompt: find('user', texts.originalPrompt),
    originalAnswer: find('assistant', texts.originalAnswer),
    resumedPrompt: find('user', texts.resumedPrompt),
  }
}

/** Require the original prompt as a user turn, then the original answer as an assistant turn, then the resumed prompt. */
export function expectNativeResumeContext(turns: readonly NativeModelTurn[], texts: NativeResumeContextTexts): void {
  const order = nativeResumeContextOrder(turns, texts)
  expect(order.originalPrompt, 'The native request must hold the original prompt in a user turn.').toBeGreaterThanOrEqual(0)
  expect(order.originalAnswer, 'The native request must hold the original answer in an assistant turn after the original prompt.').toBeGreaterThan(order.originalPrompt)
  expect(order.resumedPrompt, 'The native request must hold the resumed prompt in a user turn after the original answer.').toBeGreaterThan(order.originalAnswer)
}

/**
 * Judge the first Worker state after STARTING. A startup failure throws the startup error of the Worker. A status
 * other than ACTIVE, another provider, or another native session also throws.
 */
export function reopenedNativeAgentVerdict(agent: AgentInfo, stored: NativeResumeIdentity): AgentInfo {
  if (stored.agentSessionId.trim() === '')
    throw new Error('The native resume verdict requires the stored native session ID.')
  if (agent.status === AgentStatus.STARTUP_FAILED)
    throw new Error(`The Worker failed to start the resumed native session: ${agent.startupError.trim() || '(the Worker reported no startup error)'}`)
  if (agent.status !== AgentStatus.ACTIVE)
    throw new Error(`The resumed native agent has the Worker status ${AgentStatus[agent.status] ?? agent.status}, not ACTIVE.`)
  if (agent.agentProvider !== stored.agentProvider)
    throw new Error(`The resumed native agent has the provider ${agent.agentProvider}, not the stored provider ${stored.agentProvider}.`)
  if (agent.agentSessionId !== stored.agentSessionId)
    throw new Error(`The resumed native agent confirmed the session "${agent.agentSessionId}", not the stored session "${stored.agentSessionId}".`)
  return agent
}

/**
 * Wait for the Worker verdict on the agent that the picker opened, and require the stored native session.
 *
 * The selected tab identifies the agent. The Worker supplies the verdict. Copied transcript rows appear while the
 * agent is still STARTING, so they cannot prove a resume. A startup failure throws at once. The caller then sends no
 * input, because input to a failed agent can make the Worker launch the refused session again.
 */
export async function expectReopenedNativeAgent(
  context: Pick<ManagedNativeScenarioContext, 'page' | 'leapmuxServer'>,
  stored: NativeResumeIdentity,
  earlierAgentIds: readonly string[],
): Promise<AgentInfo> {
  const tab = selectedAgentTab(context.page)
  let agentId = ''
  await expect.poll(async () => {
    const [id] = await tab.evaluateAll(tabs => tabs.map(item => item.getAttribute('data-tab-id') ?? ''))
    agentId = id && !earlierAgentIds.includes(id) ? id : ''
    return agentId
  }, { message: 'The picker must select the agent tab that it opened.' }).not.toBe('')
  // TypeScript does not see an assignment that a callback makes to a local variable, and it narrows such a variable to
  // its initial value. A property keeps its declared type.
  const settled: { agent?: AgentInfo } = {}
  await expect.poll(async () => {
    const agent = await nativeAgentById(context, agentId)
    if (agent && agent.status !== AgentStatus.STARTING)
      settled.agent = agent
    return settled.agent !== undefined
  }, { message: 'The Worker must end the startup of the reopened agent.' }).toBe(true)
  if (!settled.agent)
    throw new Error('The Worker verdict for the reopened agent is absent.')
  return reopenedNativeAgentVerdict(settled.agent, stored)
}

/** Count the Worker rows of the original agent that hold the original answer. The original agent must be active. */
export async function countOriginalAnswerRows(
  context: Pick<ManagedNativeScenarioContext, 'leapmuxServer'>,
  agentId: string,
  texts: Pick<NativeResumeTexts, 'originalAnswer'>,
): Promise<number> {
  const rows = nativeMessagesHoldingText((await readNativeMessageSnapshot(context, agentId)).messages, texts.originalAnswer)
  if (rows.length === 0)
    throw new Error('The original agent stored no Worker row that holds the original answer.')
  return rows.length
}

/** Prove on the page that the resumed answer bubble is visible and holds no original answer. */
export async function expectUnmergedAnswerBubbles(page: Page, texts: Pick<NativeResumeTexts, 'originalAnswer' | 'resumedAnswer'>): Promise<void> {
  const resumedBubbles = assistantBubbles(page).filter({ hasText: texts.resumedAnswer })
  await expect(resumedBubbles.first()).toBeVisible()
  await expect(resumedBubbles.filter({ hasText: texts.originalAnswer }), 'The resumed answer bubble must not hold the original answer.').toHaveCount(0)
}

/** Prove the separate resumed answer, and return the Worker rows that the proof read. */
async function proveResumedAnswerUnmerged(
  context: Pick<ManagedNativeScenarioContext, 'page' | 'leapmuxServer'>,
  agentId: string,
  texts: Pick<NativeResumeTexts, 'originalAnswer' | 'resumedAnswer'>,
): Promise<AgentChatMessage[]> {
  await expectUnmergedAnswerBubbles(context.page, texts)
  const messages = (await readNativeMessageSnapshot(context, agentId)).messages
  const resumedRows = nativeMessagesHoldingText(messages, texts.resumedAnswer)
  expect(resumedRows.length, 'The resumed agent must store a Worker row that holds the resumed answer.').toBeGreaterThan(0)
  expect(nativeMessagesHoldingText(resumedRows, texts.originalAnswer).map(row => row.id), 'A Worker row of the resumed answer must not hold the original answer.').toEqual([])
  return messages
}

/**
 * Prove that the resumed answer is separate from the original answer, on the page and in the Worker rows. A native
 * replay of the original answer that the Worker merges into the next turn fails here. Use this proof alone for an
 * external native session, which opens without Worker rows to copy.
 */
export async function expectResumedAnswerUnmerged(
  context: Pick<ManagedNativeScenarioContext, 'page' | 'leapmuxServer'>,
  agentId: string,
  texts: Pick<NativeResumeTexts, 'originalAnswer' | 'resumedAnswer'>,
): Promise<void> {
  await proveResumedAnswerUnmerged(context, agentId, texts)
}

/**
 * Prove the transcript after the turn that continues a reopened LeapMux session:
 * - The resumed answer is separate from the original answer, on the page and in the Worker rows.
 * - The page shows one original prompt bubble and the original-answer bubbles the live transcript
 *   drew before the close: an answer tool can leave its marker in several row kinds (Dirac's
 *   `respond` keeps it in the request row, the result row, and the answer text row), and a stored
 *   row can also render no bubble, so the page count is its own number. A caller that states no
 *   count takes one, the number every single-bubble provider draws.
 * - The Worker keeps the original count of rows that hold the original answer, so the resume stored no replayed answer.
 */
export async function expectResumedConversation(
  context: Pick<ManagedNativeScenarioContext, 'page' | 'leapmuxServer'>,
  agentId: string,
  texts: Pick<NativeResumeTexts, 'originalPrompt' | 'originalAnswer' | 'resumedAnswer'>,
  originalAnswerRows: number,
  originalAnswerBubbles: number = 1,
): Promise<void> {
  if (!Number.isInteger(originalAnswerRows) || originalAnswerRows <= 0)
    throw new Error('The resumed conversation proof requires a positive count of original answer rows.')
  if (!Number.isInteger(originalAnswerBubbles) || originalAnswerBubbles <= 0)
    throw new Error('The resumed conversation proof requires a positive count of original answer bubbles.')
  const messages = await proveResumedAnswerUnmerged(context, agentId, texts)
  await expect(userBubbles(context.page).filter({ hasText: texts.originalPrompt })).toHaveCount(1)
  await expect(assistantBubbles(context.page).filter({ hasText: texts.originalAnswer }), 'The reopened transcript draws exactly the original answer bubbles.').toHaveCount(originalAnswerBubbles)
  expect(nativeMessagesHoldingText(messages, texts.originalAnswer).length, 'The resumed agent must store no new Worker row that holds the original answer.').toBe(originalAnswerRows)
}
