import { describe, expect, it } from 'vitest'
import { elementText } from '../../messageRenderTestUtils'
import { userTextContentRenderer } from './messageRenderers'

// A user message's body arrives in two shapes, and both are ordinary text:
//
//  - a plain string, which is what a local slash command's response carries;
//  - an array of text content blocks, which is what Claude forwards into a
//    SUBAGENT's own transcript (an interrupt notice, for one).
//
// The array shape used to reach this renderer only as an `agent_prompt`, so a
// stopped subagent's "[Request interrupted by user]" rendered inside a collapsed
// "Prompt" card -- a card claiming the message was the instruction that started
// the subagent, when it was the note that ended it.
describe('userTextContentRenderer', () => {
  it('renders a plain string body', () => {
    expect(elementText(userTextContentRenderer.render({
      type: 'user',
      message: { role: 'user', content: 'hello' },
    }, undefined))).toBe('hello')
  })

  it('renders a text content-block array', () => {
    expect(elementText(userTextContentRenderer.render({
      type: 'user',
      message: { role: 'user', content: [{ type: 'text', text: '[Request interrupted by user]' }] },
      parent_tool_use_id: 'toolu_1',
    }, undefined))).toBe('[Request interrupted by user]')
  })

  // Joined with a blank line between, so markdown renders them as separate
  // paragraphs. Asserted on the rendered text rather than the exact whitespace,
  // which the markdown pass owns.
  it('joins several text blocks as paragraphs', () => {
    const text = elementText(userTextContentRenderer.render({
      type: 'user',
      message: { role: 'user', content: [{ type: 'text', text: 'one' }, { type: 'text', text: 'two' }] },
    }, undefined))
    expect(text).toContain('one')
    expect(text).toContain('two')
    expect(text.indexOf('one')).toBeLessThan(text.indexOf('two'))
  })

  // An array that carries no renderable text yields nothing rather than an
  // empty bubble -- a tool_result array reaches a different renderer entirely.
  it('renders nothing for an array with no text blocks', () => {
    expect(userTextContentRenderer.render({
      type: 'user',
      message: { role: 'user', content: [{ type: 'image' }] },
    }, undefined)).toBeNull()
  })

  it('extracts the inner text of a local-command-stdout wrapper', () => {
    expect(elementText(userTextContentRenderer.render({
      type: 'user',
      message: { role: 'user', content: '<local-command-stdout>context: 42%</local-command-stdout>' },
    }, undefined))).toBe('context: 42%')
  })

  it('declines a body that is not a user message', () => {
    expect(userTextContentRenderer.render({ type: 'assistant', message: { content: 'x' } }, undefined)).toBeNull()
    expect(userTextContentRenderer.render({ type: 'user' }, undefined)).toBeNull()
  })
})
