import type { Page } from '@playwright/test'
import type { MockModelRequestRecord, MockModelStep } from './mockModelScript'
import type { NativeScenarioContext } from './nativeScenario'
import { expect } from '@playwright/test'
import { nativeTextStep } from './nativeScenario'
import { uniqueMarker } from './shellArguments'
import { bandRows, expectRowsInOrder, sendMessage, waitForAgentIdle } from './ui'

/**
 * Where the thought row of a turn sits, relative to the answer row of the same turn.
 * - `before`: the provider streams the reasoning first, so the thought row comes first.
 * - `after`: the provider reports the reasoning after the answer text, so the thought row follows the answer row.
 */
export type ThoughtRowOrder = 'before' | 'after'

/** The texts and the model step of one scripted reasoning turn. */
export interface ThinkingTurn {
  /** The prompt without its scenario marker. */
  prompt: string
  reasoning: string
  answer: string
  /** The step that answers the prompt: the provider's answer, with the reasoning. */
  step: MockModelStep
}

/**
 * Build one reasoning turn with a marker of its own.
 * The answer goes through the provider's text step, so an answer-tool provider (Dirac) keeps its answer tool.
 * The prompt, the reasoning, and the answer hold the marker after different words, so one text never holds another.
 */
export function thinkingTurn(context: NativeScenarioContext, marker: string = uniqueMarker()): ThinkingTurn {
  if (marker === '')
    throw new Error('A thinking turn needs a marker, so its reasoning and its answer differ from every earlier turn.')
  const reasoning = `THINKINGREASONING${marker} I check the two values first.`
  const answer = `THINKINGANSWER${marker} The values agree.`
  return {
    prompt: `Reason once, then answer with THINKINGPROMPT${marker}.`,
    reasoning,
    answer,
    step: { ...nativeTextStep(context, answer), reasoning },
  }
}

/** The texts of a turn in the order in which their rows must appear. */
export function thoughtRowTexts(turn: Pick<ThinkingTurn, 'reasoning' | 'answer'>, order: ThoughtRowOrder): [string, string] {
  return order === 'before' ? [turn.reasoning, turn.answer] : [turn.answer, turn.reasoning]
}

/**
 * Require the reasoning in a thought row of its own, the answer in a text row, no text row that holds the reasoning,
 * and the two rows in `order`. A thought row that holds the answer also fails, because the order check requires each
 * text in its own row.
 */
async function expectSeparateThoughtRow(page: Page, turn: ThinkingTurn, order: ThoughtRowOrder): Promise<void> {
  await expect(bandRows(page, 'thought').filter({ hasText: turn.reasoning }).first()).toBeVisible()
  await expect(bandRows(page, 'text').filter({ hasText: turn.answer }).first()).toBeVisible()
  // A text row is in view, so a zero count of the text rows that hold the reasoning reads real rows.
  await expect(bandRows(page, 'text').filter({ hasText: turn.reasoning })).toHaveCount(0)
  await expectRowsInOrder(bandRows(page), thoughtRowTexts(turn, order))
}

/**
 * Script one reasoning turn, and require a separate thought row, live and after reload.
 * Each check runs on both passes: the reasoning in a thought row, the answer in a text row, no reasoning in a text row,
 * and the order of the thought row and the answer row.
 * Return the turn and the model request that consumed its step, so a caller can check a provider fact of the request.
 */
export async function exerciseThinkingRows(
  context: NativeScenarioContext,
  options: { order?: ThoughtRowOrder } = {},
): Promise<ThinkingTurn & { request: MockModelRequestRecord }> {
  const order = options.order ?? 'before'
  const turn = thinkingTurn(context)
  const start = await context.modelScript.queue(turn.step)
  await sendMessage(context.page, context.modelScript.prompt(turn.prompt))
  await context.modelScript.waitForSteps(start + 1)
  await waitForAgentIdle(context.page)
  await expectSeparateThoughtRow(context.page, turn, order)
  await context.page.reload()
  await expectSeparateThoughtRow(context.page, turn, order)
  // Read the record after the turn. A native client can add fields to a request after the mock counts its step.
  return { ...turn, request: await context.modelScript.requestAt(start) }
}
