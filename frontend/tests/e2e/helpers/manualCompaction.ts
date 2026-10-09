import type { MockModelMatcher, MockModelRequestRecord, MockModelStep, MockModelUsage } from './mockModelScript'
import type { NativeScenarioContext } from './nativeScenario'
import { randomUUID } from 'node:crypto'
import { expect } from '@playwright/test'
import { escapeRegExp } from '../../../src/lib/regexp'
import { nativeScenarioModelContextText, nativeTextStep } from './nativeScenario'
import { assistantBubbles, messageBubbles, sendMessage, waitForAgentIdle } from './ui'

/**
 * The matcher pattern of the directive that the summarizer prompt of Claude Code's own `/compact` holds:
 * "CRITICAL: Respond with TEXT ONLY". Qoder CLI, which derives from Claude Code, sends the same directive. A matcher
 * pattern is a regular expression, so the pattern escapes the text.
 */
export const CLAUDE_SUMMARIZER_PATTERN = escapeRegExp('CRITICAL: Respond with TEXT ONLY')

export const MANUAL_COMPACTION_MARKER = 'CEDAR_MANUAL_SUMMARY'
export const MANUAL_COMPACTION_SUMMARY = [
  `${MANUAL_COMPACTION_MARKER}: the cedar marker belongs to the older work.`,
  ...Array.from({ length: 30 }, (_, index) => `Earlier item ${index} records cedar detail ${index * 7} for the next assistant.`),
].join(' ')

/** The marker of the old context, which the first seed answer holds and the summary must replace. */
export const OLDER_CONTEXT_MARKER = 'OLDER_CONTEXT item 0:'

const olderContext = Array.from({ length: 300 }, (_, index) => `OLDER_CONTEXT item ${index}: cedar detail ${index * 7}.`).join(' ')

/**
 * The answer of the second seed turn. A provider that replaces the whole history with the summary drops it too, while
 * a provider that keeps its recent turns keeps it.
 */
export const COMPACTION_NEWER_ANSWER = 'The newer note is about the parser.'

/** The three turns before the compaction. The first answer holds the old context that the summary replaces. */
export const COMPACTION_SEED_TURNS: readonly { prompt: string, answer: string }[] = [
  { prompt: 'Record the older context.', answer: olderContext },
  { prompt: 'Record a newer note.', answer: COMPACTION_NEWER_ANSWER },
  { prompt: 'Record the current note.', answer: 'The current note is about the tests.' },
]

/**
 * How the native summarizer request of a provider reaches the mock model:
 * - `fallback`: the request falls through to the fallback, which answers the summary. The proof requires
 *   `requestMarker` in a fallback request, or `completionText` in the transcript for a request that the proof cannot
 *   read.
 * - `rule`: a rule answers each request that `when` matches. Use it for a summarizer that can run more than once.
 * - `queued`: the summarizer takes the next ordered step.
 */
export type CompactionSummaryRoute
  = | { route: 'fallback', requestMarker: string }
    | { route: 'fallback', completionText: string }
    | { route: 'rule', when: MockModelMatcher, respond?: MockModelStep }
    | { route: 'queued' }

/** The provider facts of one native compaction. Each provider states them once, for both compaction cells. */
export interface NativeCompactionOptions {
  summary: CompactionSummaryRoute
  /** Put the summary into the reply format that the native summarizer requires. The result must keep the summary. */
  summaryEnvelope?: (summary: string) => string
  /** The usage of the summary reply, for a client that compares the summary with the history size. */
  summaryUsage?: MockModelUsage
  /**
   * The input tokens that the first seed turn reports. Each later turn reports 500 more. A client that compares its
   * history size with the summary needs this, because the mock reports one token by default.
   */
  reportedInputTokens?: number
}

/** The requests that a native compaction scenario read. */
export interface NativeCompactionResult {
  /** The native summarizer request. Only the `queued` route reads it. */
  summaryRequest?: MockModelRequestRecord
  /** The request of the first turn after the compaction. */
  nextRequest: MockModelRequestRecord
}

/** Seed the three conversation turns before the native compaction request. Each turn counts from its own queue index. */
export async function seedManualCompactionConversation(context: NativeScenarioContext, reportedInputTokens?: number): Promise<void> {
  const { page, modelScript } = context
  for (const [index, { prompt, answer }] of COMPACTION_SEED_TURNS.entries()) {
    const stepIndex = await modelScript.queue({
      ...nativeTextStep(context, answer),
      ...(reportedInputTokens !== undefined ? { usage: { inputTokens: reportedInputTokens + index * 500, outputTokens: Math.max(1, Math.ceil(answer.length / 4)) } } : {}),
    })
    await sendMessage(page, modelScript.prompt(prompt))
    if (index === 1) {
      // The newer turn carries the older context to the model.
      expect(nativeScenarioModelContextText(context, await modelScript.requestAt(stepIndex))).toContain(OLDER_CONTEXT_MARKER)
    }
    await modelScript.waitForSteps(stepIndex + 1)
    await waitForAgentIdle(page)
  }
}

/** The summary reply of one scenario. It always holds {@link MANUAL_COMPACTION_MARKER}. */
export function compactionSummaryText(options: Pick<NativeCompactionOptions, 'summaryEnvelope'>): string {
  const text = options.summaryEnvelope ? options.summaryEnvelope(MANUAL_COMPACTION_SUMMARY) : MANUAL_COMPACTION_SUMMARY
  if (!text.includes(MANUAL_COMPACTION_SUMMARY))
    throw new Error('The summary envelope must keep the whole summary, which holds the summary marker.')
  return text
}

/**
 * Run one native `/compact` and prove that the summary replaces the old context in the next turn.
 *
 * The scenario seeds three turns, sends `/compact`, proves the summarizer request through its route, and then runs
 * one more turn. That turn must carry the summary and must not carry the old context. Every request index comes from
 * the queue, so an earlier turn of the test changes nothing. The caller checks the notice, which differs by provider.
 */
export async function exerciseNativeCompaction(context: NativeScenarioContext, options: NativeCompactionOptions): Promise<NativeCompactionResult> {
  const { page, modelScript } = context
  const summary = { text: compactionSummaryText(options), ...(options.summaryUsage ? { usage: options.summaryUsage } : {}) }
  await seedManualCompactionConversation(context, options.reportedInputTokens)
  const route = options.summary
  const ruleName = `native-compaction-summary-${randomUUID()}`
  let summaryStep: number | undefined
  if (route.route === 'fallback')
    await modelScript.fallback(summary)
  else if (route.route === 'rule')
    await modelScript.rule({ name: ruleName, when: route.when, respond: route.respond ?? summary })
  else
    summaryStep = await modelScript.queue(summary)
  await sendMessage(page, '/compact')

  let summaryRequest: MockModelRequestRecord | undefined
  if (route.route === 'fallback') {
    await expect.poll(async () => (await modelScript.status()).requests.some(request => request.fallback)).toBe(true)
    await waitForAgentIdle(page)
    if ('requestMarker' in route) {
      const requests = (await modelScript.status()).requests.filter(request => request.fallback)
      expect(requests.some(request => (JSON.stringify(request.body) ?? '').includes(route.requestMarker)), 'a native summary request reached the model').toBe(true)
    }
    else {
      await expect(messageBubbles(page).filter({ hasText: route.completionText }).first()).toBeVisible()
    }
  }
  else if (route.route === 'rule') {
    await expect.poll(async () => (await modelScript.status()).ruleMatches[ruleName] ?? 0, 'a native summary request reached the model').toBeGreaterThan(0)
    await waitForAgentIdle(page)
  }
  else {
    if (summaryStep === undefined)
      throw new Error('The queued summary route queued no summary step.')
    await modelScript.waitForSteps(summaryStep + 1)
    await waitForAgentIdle(page)
    summaryRequest = await modelScript.requestAt(summaryStep)
  }

  const answer = 'The compacted session continued.'
  const stepIndex = await modelScript.queue(nativeTextStep(context, answer))
  await sendMessage(page, modelScript.prompt('Continue after the context summary.'))
  await modelScript.waitForSteps(stepIndex + 1)
  await waitForAgentIdle(page)
  // The follow-up prompt reaches the model with the summary in place of the older context.
  const nextRequest = await modelScript.requestAt(stepIndex)
  const nextContext = nativeScenarioModelContextText(context, nextRequest)
  expect(nextContext).toContain(MANUAL_COMPACTION_MARKER)
  expect(nextContext).not.toContain(OLDER_CONTEXT_MARKER)
  await expect(assistantBubbles(page).filter({ hasText: answer }).first()).toBeVisible()
  return { ...(summaryRequest ? { summaryRequest } : {}), nextRequest }
}
