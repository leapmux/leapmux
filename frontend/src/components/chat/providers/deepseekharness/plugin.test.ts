import { describe, expect, it } from 'vitest'
import { DEEPSEEK_HARNESS_TOOL } from '~/generated/contracts/deepseek-harness-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import { classifyDeepseekHarnessMessage } from './classification'
import { deepseekHarnessCompactionBoundary, deepseekHarnessNotificationEntry } from './extractors/notification'
import { deepseekHarnessResultDivider } from './extractors/resultDivider'
import { deepseekHarnessSpanRole } from './spanRole'
import { deepseekHarnessToolKind } from './toolKinds'
import './plugin'

const provider = AgentProvider.DEEPSEEK_HARNESS
const event = (type: string, data: Record<string, unknown> = {}) => ({ type, seq: 1, time: 1000, data })

describe('deepseek harness provider', () => {
  it('declares separate native plan and permission axes', () => {
    const plugin = providerFor(provider)
    expect(plugin?.configuration?.attachments).toEqual({ text: true, image: true, pdf: true, binary: true })
    expect(plugin?.configuration?.planMode?.currentMode({})).toBe('act')
    expect(plugin?.configuration?.planMode?.currentMode({ optionValues: { permissionMode: 'plan' } })).toBe('plan')
    expect(plugin?.controls?.permissionPresets).toEqual({ bypass: { sets: { permissions: 'danger-full-access' } } })
    expect(plugin?.controls?.askUserQuestion).toBeDefined()
  })

  it.each([
    { frame: { ...event('assistant/message', { message: { content: [{ type: 'reasoning', text: 'Native reasoning' }] } }), blockIndex: 0 }, kind: 'assistant_thinking' },
    { frame: { ...event('assistant/message', { message: { content: [{ type: 'text', text: 'Native answer' }] } }), blockIndex: 0 }, kind: 'assistant_text' },
    { frame: { ...event('assistant/message', { message: { content: [{ type: 'text', text: '' }] } }), blockIndex: 0 }, kind: 'hidden' },
    { frame: event('assistant/message', { message: { content: [{ type: 'text', text: 'Ambiguous block' }] } }), kind: 'unknown' },
    { frame: event('tool/call', { callId: 'call', name: 'bash' }), kind: 'tool_use' },
    { frame: event('tool/result', { message: { toolCallId: 'call', content: [] } }), kind: 'tool_result' },
    { frame: event('tool/result', { callId: 'wrong-shape' }), kind: 'unknown' },
    { frame: event('user/message', { role: 'user', content: [{ type: 'text', text: 'Child prompt' }] }), kind: 'user_content' },
    { frame: event('turn/end', { reason: { kind: 'completed' } }), kind: 'result_divider' },
    { frame: event('compaction/end'), kind: 'notification' },
    { frame: event('tool-workflow/run-start', { runId: 'run', name: 'Native workflow' }), kind: 'hidden' },
    { frame: event('future/event'), kind: 'unknown' },
  ])('classifies the native event as $kind', ({ frame, kind }) => {
    expect(classifyDeepseekHarnessMessage(input(frame, null, provider)).kind).toBe(kind)
  })

  it('reads each completed compaction in a notification thread', () => {
    const completed = event('compaction/end')
    const failed = event('compaction/end', { error: 'Native failure' })
    const result = classifyDeepseekHarnessMessage(input(undefined, { old_seqs: [], messages: [completed, failed] }, provider))
    expect(result).toMatchObject({ kind: 'notification', entries: [
      { kind: 'compaction', phase: 'end' },
      { kind: 'text', text: 'Native compaction failed: Native failure' },
    ] })
    expect(deepseekHarnessCompactionBoundary(input(completed, null, provider))).toEqual({})
    expect(deepseekHarnessCompactionBoundary(input(failed, null, provider))).toBeNull()
    expect(deepseekHarnessNotificationEntry(event('compaction/start'))).toEqual([])
  })

  it('uses completion to identify a call retained at an interrupted turn', () => {
    const call = input(event('tool/call', { callId: 'call', name: 'bash' }), null, provider)
    expect(deepseekHarnessSpanRole(call)).toBe('request')
    expect(deepseekHarnessSpanRole({ ...call, completion: MessageCompletion.INTERRUPTED })).toBe('result')
    expect(classifyDeepseekHarnessMessage({ ...call, completion: MessageCompletion.INTERRUPTED })).toEqual({ kind: 'tool_result' })
    expect(deepseekHarnessSpanRole(input(event('tool/result', { message: { toolCallId: 'call' } }), null, provider))).toBe('result')
    expect(deepseekHarnessSpanRole(input(event('tool/result'), null, provider))).toBe('other')
  })

  it('reads completed, interrupted, and failed native turn boundaries', () => {
    expect(deepseekHarnessResultDivider(event('turn/end', { reason: { kind: 'completed' } }))).toMatchObject({ label: expect.stringContaining('Turn ended') })
    expect(deepseekHarnessResultDivider(event('turn/end', { reason: { kind: 'aborted', reason: { kind: 'user' } } }))).toMatchObject({ label: expect.stringContaining('Turn interrupted') })
    expect(deepseekHarnessResultDivider(event('turn/end', { reason: { kind: 'error', error: { message: 'Native failure', code: 'UNKNOWN' } } }))).toMatchObject({ isError: true, label: expect.stringContaining('Native failure') })
    expect(deepseekHarnessResultDivider(event('turn/start'))).toBeNull()
  })

  it('maps every native tool and does not read inherited object properties', () => {
    for (const tool of Object.values(DEEPSEEK_HARNESS_TOOL))
      expect(deepseekHarnessToolKind(tool)).not.toBe('unspecified')
    expect(deepseekHarnessToolKind('constructor')).toBe('unspecified')
    expect(deepseekHarnessToolKind('toString')).toBe('unspecified')
  })

  it('shows the exact selected native answer in its saved control row', () => {
    const request = { type: 'waterfall', event: 'user-questions/request', eventId: 'native-event', agentId: 'native-root', request: { questions: [{ id: 'route', question: 'Choose a route', options: [{ label: 'First' }, { label: 'Second' }] }] } }
    const response = { type: 'control_response', response: { subtype: 'success', request_id: 'native-event', response: { behavior: 'allow', answers: [{ id: 'route', selected: ['Second'] }] } } }
    const display = providerFor(provider)?.controls?.controlResponseDisplay?.({ requestId: 'native-event', claimToken: 'native-claim', request, response })
    expect(display).toEqual({ kind: 'label', text: 'Choose a route: Second' })
  })
})
