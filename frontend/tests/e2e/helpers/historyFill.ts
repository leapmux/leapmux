import type { Page } from '@playwright/test'
import type { AgentChatMessage } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ModelScript } from './modelScriptFixture'
import type { ScriptedTurn } from './scriptedTurn'
import { expect } from '@playwright/test'
import { sendScriptedTurn } from './scriptedTurn'

/** The message sequences of each span of this type, in transcript order. */
export function spanRowSequences(messages: readonly Pick<AgentChatMessage, 'spanType' | 'spanId' | 'seq'>[], spanType: string): bigint[][] {
  const spans = new Map<string, bigint[]>()
  for (const message of messages) {
    if (message.spanType !== spanType)
      continue
    const rows = spans.get(message.spanId)
    if (rows === undefined)
      spans.set(message.spanId, [message.seq])
    else
      rows.push(message.seq)
  }
  return Array.from(spans.values())
}

/**
 * Filler turns that put a row at an exact place in the pages of the history.
 *
 * The Worker pages the history by a count of rows (MESSAGE_PAGE_LIMIT), so a spec that
 * needs a row first in a page, or last in one, must store an exact number of rows around
 * it. A plain turn and a turn with reasoning store different numbers of rows in a provider
 * that stores its reasoning as a row of its own, as Codex does. The first turn of each kind
 * measures its number, and the rest of the count splits into turns of the two kinds.
 */

/** A short turn whose prompt and answer state its number. A turn with reasoning stores one row more in Codex. */
export function fillerTurn(turn: number, reasoning: boolean): ScriptedTurn {
  return {
    prompt: `Filler ${turn}: reply with its number.`,
    answer: `Filler answer ${turn}.`,
    entry: 'insert',
    ...(reasoning
      ? { reasoning: `Thinking about filler ${turn}.` }
      : {}),
  }
}

/** The numbers of plain turns and of turns with reasoning that store a count of rows. */
export interface TurnSplit {
  plain: number
  reasoning: number
}

/**
 * Split `rows` into turns of `plainRows` and of `reasoningRows` rows, with the fewest turns
 * with reasoning. Null when no split exists: `rows` is negative, or the two sizes cannot
 * sum to it. Both sizes must be positive integers.
 */
export function splitRowsIntoTurns(rows: number, plainRows: number, reasoningRows: number): TurnSplit | null {
  if (!Number.isInteger(plainRows) || !Number.isInteger(reasoningRows) || plainRows <= 0 || reasoningRows <= 0)
    throw new Error('A turn size must be a positive integer.')
  if (!Number.isInteger(rows) || rows < 0)
    return null
  for (let reasoning = 0; reasoning * reasoningRows <= rows; reasoning++) {
    const rest = rows - reasoning * reasoningRows
    if (rest % plainRows === 0)
      return { plain: rest / plainRows, reasoning }
  }
  return null
}

/** What `fillRows` needs: the page and the script of the chat, and the count that the turns raise. */
export interface RowFill {
  page: Page
  modelScript: ModelScript
  /** The count of rows that the turns raise, read through the Worker. */
  count: () => Promise<number>
  /** The count to reach. */
  target: number
  /** The number of the first filler turn, so two fills of one chat send different prompts. */
  firstTurn: number
}

/**
 * Send filler turns until `count()` is exactly `target`. Fails when the count passes the
 * target, or when the rest cannot split into turns of the measured sizes. Returns the number
 * of the last turn that it sent, or `firstTurn - 1` when it sent none.
 */
export async function fillRows(fill: RowFill): Promise<number> {
  let turn = fill.firstTurn - 1
  const send = (reasoning: boolean) => sendScriptedTurn(fill.page, fill.modelScript, fillerTurn(++turn, reasoning))
  const initial = await fill.count()
  if (initial === fill.target)
    return turn
  await send(false)
  const afterPlain = await fill.count()
  await send(true)
  const afterReasoning = await fill.count()
  const plainRows = afterPlain - initial
  const reasoningRows = afterReasoning - afterPlain
  const remaining = fill.target - afterReasoning
  const split = splitRowsIntoTurns(remaining, plainRows, reasoningRows)
  expect(split, `the ${remaining} rows still to fill split into turns of ${plainRows} and ${reasoningRows} rows`).not.toBeNull()
  for (let i = 0; i < split!.reasoning; i++)
    await send(true)
  for (let i = 0; i < split!.plain; i++)
    await send(false)
  expect(await fill.count(), `the filler turns reach ${fill.target} rows`).toBe(fill.target)
  return turn
}
