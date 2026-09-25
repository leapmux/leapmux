import { describe, expect, it } from 'vitest'
import { CODEWHALE_BLOCK_TYPE, CODEWHALE_EVENT, CODEWHALE_ITEM_KIND, CODEWHALE_TRANSCRIPT_ROLE } from '~/generated/contracts/codewhale-protocol'
import { childBlock, codewhaleEvent, itemFinished } from '../toolResults.fixtures'
import { codewhaleMessageText } from './message'

describe('codewhaleMessageText', () => {
  it('reads the final event of a reply and of a reasoning step', () => {
    expect(codewhaleMessageText(itemFinished(CODEWHALE_ITEM_KIND.AgentMessage, 'Hello.'))).toStrictEqual({ kind: 'text', text: 'Hello.' })
    expect(codewhaleMessageText(itemFinished(CODEWHALE_ITEM_KIND.AgentReasoning, 'Hmm.'))).toStrictEqual({ kind: 'thinking', text: 'Hmm.' })
  })

  it('reads an interrupted reply too', () => {
    expect(codewhaleMessageText(itemFinished(CODEWHALE_ITEM_KIND.AgentMessage, 'Hel', {}, CODEWHALE_EVENT.ItemInterrupted))).toStrictEqual({ kind: 'text', text: 'Hel' })
    expect(codewhaleMessageText(itemFinished(CODEWHALE_ITEM_KIND.AgentReasoning, 'Hm', {}, CODEWHALE_EVENT.ItemFailed))).toStrictEqual({ kind: 'thinking', text: 'Hm' })
  })

  // The classifier decides what a blank message draws, so this reader must hand it
  // the text unchanged rather than answer null for it.
  it('returns the text as the runtime wrote it, blank or padded', () => {
    expect(codewhaleMessageText(itemFinished(CODEWHALE_ITEM_KIND.AgentMessage, ''))).toStrictEqual({ kind: 'text', text: '' })
    expect(codewhaleMessageText(itemFinished(CODEWHALE_ITEM_KIND.AgentMessage, '\n  Hello.\n'))).toStrictEqual({ kind: 'text', text: '\n  Hello.\n' })
    expect(codewhaleMessageText(childBlock(CODEWHALE_TRANSCRIPT_ROLE.Assistant, { type: CODEWHALE_BLOCK_TYPE.Text }))).toStrictEqual({ kind: 'text', text: '' })
  })

  // `summary` repeats the head of the text, cut at 280 characters, so a long reply
  // takes its whole body from `detail`.
  it('reads a long reply from its detail, never its cut summary', () => {
    const long = 'x'.repeat(10_000)
    expect(codewhaleMessageText(itemFinished(CODEWHALE_ITEM_KIND.AgentMessage, long))?.text).toHaveLength(10_000)
  })

  it('reads a subagent\'s assistant blocks', () => {
    expect(codewhaleMessageText(childBlock(CODEWHALE_TRANSCRIPT_ROLE.Assistant, { type: CODEWHALE_BLOCK_TYPE.Text, text: 'Done.' }))).toStrictEqual({ kind: 'text', text: 'Done.' })
    expect(codewhaleMessageText(childBlock(CODEWHALE_TRANSCRIPT_ROLE.Assistant, { type: CODEWHALE_BLOCK_TYPE.Thinking, thinking: 'Hmm.' }))).toStrictEqual({ kind: 'thinking', text: 'Hmm.' })
  })

  it('answers null for any other row', () => {
    expect(codewhaleMessageText(codewhaleEvent(CODEWHALE_EVENT.ItemStarted, { item: { kind: CODEWHALE_ITEM_KIND.AgentMessage, detail: '' } }))).toBeNull()
    expect(codewhaleMessageText(itemFinished(CODEWHALE_ITEM_KIND.Status, 'x'))).toBeNull()
    expect(codewhaleMessageText(childBlock(CODEWHALE_TRANSCRIPT_ROLE.User, { type: CODEWHALE_BLOCK_TYPE.Text, text: 'x' }))).toBeNull()
    expect(codewhaleMessageText(childBlock(CODEWHALE_TRANSCRIPT_ROLE.Assistant, { type: 'image' }))).toBeNull()
    expect(codewhaleMessageText({ content: 'user text' })).toBeNull()
    expect(codewhaleMessageText(null)).toBeNull()
  })
})
