import type { MessageCategory } from './messageClassification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { defaultMarkPreview } from './markPreviewShared'

function parsedOf(obj: Record<string, unknown> | undefined): ParsedMessageContent {
  return { rawText: obj ? JSON.stringify(obj) : '', topLevel: obj ?? null, parentObject: obj, wrapper: null }
}

const USER: MessageCategory = { kind: 'user_content' }
const CONTROL: MessageCategory = { kind: 'control_response' }

describe('default mark preview', () => {
  it('extracts the provider-neutral {content} user shape', () => {
    expect(defaultMarkPreview(USER, parsedOf({ content: 'hello world' }))).toBe('hello world')
  })

  it('does NOT extract the Claude {message:{content}} transcript shape (that is the claude plugin\'s job)', () => {
    // The nested `{message:{content}}` envelope is an Anthropic transcript shape; the shared
    // neutral default handles only `{content}` / `{controlResponse}`. See claude/plugin.test.ts.
    expect(defaultMarkPreview({ kind: 'user_text' }, parsedOf({ message: { content: 'typed text' } }))).toBeNull()
  })

  it('summarizes an approved control response', () => {
    expect(defaultMarkPreview(CONTROL, parsedOf({ controlResponse: { action: 'approved' } }))).toBe('Approved')
  })

  it('summarizes a rejected control response carrying feedback the same way the row renderer does', () => {
    // Mirror controlResponseRenderer (notificationRenderers), the row the dot jumps to: a
    // rejection WITH a typed reason renders "Sent feedback:" above the reason, so the preview
    // must read the same -- NOT the inline ControlResponseTag's "Rejected: <reason>".
    expect(defaultMarkPreview(CONTROL, parsedOf({ controlResponse: { action: 'rejected', comment: 'not this way' } })))
      .toBe('Sent feedback:\nnot this way')
  })

  it('labels a bare rejection', () => {
    expect(defaultMarkPreview(CONTROL, parsedOf({ controlResponse: { action: 'rejected' } }))).toBe('Rejected')
  })

  it('returns null when there is no previewable text', () => {
    expect(defaultMarkPreview(USER, parsedOf({ foo: 'bar' }))).toBeNull()
    expect(defaultMarkPreview(USER, parsedOf(undefined))).toBeNull()
  })

  it('never mis-picks an assistant content-block array as user text (no neutral string field)', () => {
    // Assistant messages store content as a block array; the neutral string test must skip
    // them. The shared default handles ONLY neutral shapes -- Anthropic tool_result blocks
    // are a Claude-specific shape handled by the claude plugin's previewText, not here.
    expect(defaultMarkPreview({ kind: 'assistant_text' }, parsedOf({ content: [{ type: 'text', text: 'hi' }] }))).toBeNull()
    expect(defaultMarkPreview({ kind: 'assistant_text' }, parsedOf({ message: { content: [{ type: 'text', text: 'hi' }] } }))).toBeNull()
  })

  it('does NOT extract Anthropic tool_result blocks (that is the claude plugin\'s job)', () => {
    // The shared default is provider-neutral; a bare tool_result array yields no preview
    // here. The Claude plugin's previewText reads it (see claude/plugin.test.ts).
    const parsed = parsedOf({
      type: 'user',
      message: { role: 'user', content: [{ type: 'tool_result', content: 'answer', tool_use_id: 't1' }] },
    })
    expect(defaultMarkPreview({ kind: 'tool_result' }, parsed)).toBeNull()
  })
})
