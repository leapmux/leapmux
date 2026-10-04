import type { MessageCategory } from '../../../messageClassifier'
import type { ToolSpanContext } from '../../../rowExtractionTypes'
import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { resolveMessageForRendering } from '../../registry'
import { input } from '../../testUtils'
import { clineToolFinishRow, clineToolStartRow } from '../toolResults.fixtures'
import { clineExtractRow } from './row'
import '~/components/chat/providers'

const provider = AgentProvider.CLINE
const noSpan: ToolSpanContext = { request: undefined, result: undefined, role: 'result', visibleRows: { request: false, result: true } }

function extract(parent: Record<string, unknown>, kind: MessageCategory['kind'], span: ToolSpanContext = noSpan, completion?: MessageCompletion) {
  const resolved = resolveMessageForRendering({ rawText: '', topLevel: parent, parentObject: parent, wrapper: null, ...(completion !== undefined ? { completion } : {}) }, provider)
  const category: MessageCategory = kind === 'notification' ? { kind, entries: [] } : kind === 'control_response' ? { kind: 'unknown' } : { kind }
  return clineExtractRow({ category, resolved, span })
}

const event = (name: string, payload: Record<string, unknown> = {}) => ({ version: 'v1', event: name, sessionId: 's1', payload })

describe('clineExtractRow', () => {
  it('reads the text and the reasoning of a message', () => {
    expect(extract(event('assistant.finished', { text: 'done' }), 'assistant_text')).toEqual({ kind: 'assistant-text', text: 'done' })
    expect(extract(event('reasoning.finished', { reasoning: 'Think.' }), 'assistant_thinking')).toEqual({ kind: 'assistant-thinking', text: 'Think.' })
    expect(extract(event('assistant.finished', { text: '  ' }), 'assistant_text')).toEqual({ kind: 'hidden' })
    expect(extract(event('reasoning.finished', { reasoning: '' }), 'assistant_thinking')).toEqual({ kind: 'hidden' })
  })

  it('states the media a message returned', () => {
    expect(extract(event('assistant.media', { media: { type: 'image', mediaType: 'image/png', data: 'AAAA' } }), 'assistant_text'))
      .toEqual({ kind: 'assistant-text', text: 'The model returned media (image/png).' })
    expect(extract(event('assistant.media', { media: {} }), 'assistant_text')).toEqual({ kind: 'assistant-text', text: 'The model returned media.' })
  })

  it('states the media type under each spelling, the most precise first', () => {
    expect(extract(event('assistant.media', { media: { type: 'image', mimeType: 'image/jpeg' } }), 'assistant_text'))
      .toEqual({ kind: 'assistant-text', text: 'The model returned media (image/jpeg).' })
    expect(extract(event('assistant.media', { media: { type: 'audio' } }), 'assistant_text'))
      .toEqual({ kind: 'assistant-text', text: 'The model returned media (audio).' })
  })

  // A text row of the other event, or a media row with no media, states nothing.
  it('hides a text row that states neither text nor media', () => {
    expect(extract(event('assistant.media', {}), 'assistant_text')).toEqual({ kind: 'hidden' })
    expect(extract(event('assistant.media', { media: 'image' }), 'assistant_text')).toEqual({ kind: 'hidden' })
    expect(extract(event('reasoning.finished', { text: 'x' }), 'assistant_text')).toEqual({ kind: 'hidden' })
  })

  it('reads LeapMux\'s own user row', () => {
    expect(extract({ content: 'hello' }, 'user_content')).toMatchObject({ kind: 'user', text: 'hello' })
  })

  it('reads LeapMux\'s own plan execution row', () => {
    expect(extract({ content: 'Run the plan.', planExecution: true }, 'plan_execution')).toEqual({ kind: 'plan-execution', text: 'Run the plan.' })
  })

  it('pairs a result with the call of the same id only', () => {
    const result = clineToolFinishRow('run_commands', [{ query: 'printf a', result: 'a', success: true }], undefined, 'call_a')
    const mine = input(clineToolStartRow('run_commands', { commands: ['printf a'] }, 'call_a'), undefined, provider)
    const sibling = input(clineToolStartRow('run_commands', { commands: ['printf b'] }, 'call_b'), undefined, provider)
    const paired = extract(result, 'tool_result', { ...noSpan, request: mine })
    expect(paired?.kind === 'tool' ? paired.call.request : null).toMatchObject({ command: 'printf a' })
    const unpaired = extract(result, 'tool_result', { ...noSpan, request: sibling })
    // A result whose start the store did not resolve still names its own tool.
    expect(unpaired?.kind === 'tool' ? unpaired.call.kind : null).toBe('execute')
  })

  it('draws a running call as the request side, and its landed result on it', () => {
    const call = clineToolStartRow('run_commands', { commands: ['ls'] }, 'call_a')
    const running = extract(call, 'tool_use', { ...noSpan, role: 'request' })
    expect(running).toMatchObject({ kind: 'tool', role: 'request' })
    expect(running?.kind === 'tool' ? running.call.status : null).toBe('unstated')
    const landed = input(clineToolFinishRow('run_commands', [{ query: 'ls', result: 'x', success: true }], undefined, 'call_a'), undefined, provider)
    const answered = extract(call, 'tool_use', { ...noSpan, role: 'request', result: landed })
    expect(answered?.kind === 'tool' ? answered.call.status : null).toBe('completed')
  })

  it('draws a start that the turn outlived as the call\'s end', () => {
    const call = clineToolStartRow('run_commands', { commands: ['sleep 40'] }, 'call_a')
    const stopped = extract(call, 'tool_result', noSpan, MessageCompletion.INTERRUPTED)
    expect(stopped).toMatchObject({ kind: 'tool', role: 'result' })
    expect(stopped?.kind === 'tool' ? stopped.call.status : null).toBe('cancelled')
  })

  it('answers null for a tool category on a row that is not a tool row', () => {
    expect(extract(event('run.completed'), 'tool_use')).toBeNull()
  })

  it('answers null for a category it does not draw', () => {
    expect(extract(event('run.completed'), 'result_divider')).toBeNull()
  })
})
