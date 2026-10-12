import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import { classifyCommandCodeMessage } from './classification'
import { commandCodeNotificationEntry } from './extractors/notification'
import { commandCodeResultDivider } from './extractors/resultDivider'
import { commandCodeSpanRole } from './spanRole'
import './plugin'

const provider = AgentProvider.COMMAND_CODE
const event = (type: string, fields: Record<string, unknown> = {}) => ({ type: 'event', seq: 1, event: { type, ...fields } })

describe('command code provider', () => {
  it('declares the native attachment and permission options', () => {
    const plugin = providerFor(provider)
    expect(plugin?.configuration?.attachments).toEqual({ text: true, image: true, pdf: false, binary: false })
    expect(plugin?.configuration?.planMode?.currentMode({})).toBe('default')
    expect(plugin?.configuration?.planMode?.currentMode({ optionValues: { permissionMode: 'plan' } })).toBe('plan')
    expect(plugin?.controls?.permissionPresets).toEqual({ bypass: { sets: { permissionMode: 'bypass' } } })
    expect(plugin?.controls?.askUserQuestion).toBeUndefined()
    expect(plugin?.configuration?.supportsSubagentSend).toBeUndefined()
  })

  it.each([
    [event('message_end', { content: [{ type: 'text', text: 'Native answer.' }] }), 'assistant_text'],
    [event('message_end', { content: [] }), 'hidden'],
    [event('thinking_end', { text: 'Native reasoning.' }), 'assistant_thinking'],
    [event('tool_queued', { toolCallId: 'call', toolName: 'shell_command' }), 'tool_use'],
    [event('tool_completed', { toolCallId: 'call', toolName: 'shell_command', result: [] }), 'tool_result'],
    [event('tool_errored', { toolCallId: 'call', error: 'Native error.' }), 'tool_result'],
    [event('api_retry', { attempt: 0, delayMs: 0, error: 'Native retry.' }), 'notification'],
    [event('subagent_progress', { toolCallId: 'spawn', toolName: 'read_file', toolInput: '/work/file' }), 'notification'],
    [event('run_end', { result: { nextState: [{ text: 'Repeated context.' }] } }), 'hidden'],
    [event('new_native_event'), 'unknown'],
    [{ event: { type: 'message_end', content: [{ type: 'text', text: 'Wrong envelope.' }] } }, 'unknown'],
    [{ jsonrpc: '2.0', method: 'turn/completed', params: { turnId: 'turn_1', stopReason: 'end_turn' } }, 'result_divider'],
  ] satisfies Array<[Record<string, unknown>, string]>)('classifies a captured native frame as %s', (frame, category) => {
    expect(classifyCommandCodeMessage(input(frame, null, provider)).kind).toBe(category)
  })

  it('reads native event notification threads', () => {
    const retry = event('api_retry', { attempt: 0, delayMs: 0, error: 'Native retry.' })
    const category = classifyCommandCodeMessage(input(undefined, { old_seqs: [], messages: [retry] }, provider))
    expect(category.kind).toBe('notification')
    expect(category).toMatchObject({ entries: [{ kind: 'retry', attempt: 0, delayMs: 0, error: 'Native retry.' }] })
  })

  it('keeps retry zero values and omits absent values', () => {
    expect(commandCodeNotificationEntry(event('api_retry', { attempt: 0, delayMs: 0, error: { message: 'Native retry.' } }))).toEqual([{ kind: 'retry', scope: 'api', attempt: 0, delayMs: 0, error: 'Native retry.' }])
    expect(commandCodeNotificationEntry(event('api_retry'))).toEqual([{ kind: 'retry', scope: 'api', error: '' }])
  })

  it('pairs only native opening and finished tool frames', () => {
    expect(commandCodeSpanRole(input(event('tool_queued', { toolCallId: 'call' }), null, provider))).toBe('request')
    expect(commandCodeSpanRole(input(event('tool_completed', { toolCallId: 'call' }), null, provider))).toBe('result')
    expect(commandCodeSpanRole(input(event('tool_update', { toolCallId: 'call' }), null, provider))).toBe('other')
    expect(commandCodeSpanRole(input(event('tool_completed'), null, provider))).toBe('other')
  })

  it('shows an explicit native failure at the turn boundary', () => {
    expect(commandCodeResultDivider({ method: 'turn/completed', params: { stopReason: 'run_error', error: { message: 'Native connection failed.' } } })).toMatchObject({ isError: true, label: expect.stringContaining('Native connection failed.') })
    expect(commandCodeResultDivider({ method: 'another/method' })).toBeNull()
  })
})
