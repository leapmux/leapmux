import { describe, expect, it } from 'vitest'
import { PI_EVENT, PI_NOTIFICATION_EVENT } from '~/generated/contracts/pi-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import './plugin'

/** Supply the content that each stored row requires. */
const PAYLOAD: Record<string, Record<string, unknown>> = {
  [PI_EVENT.MessageEnd]: { message: { role: 'assistant', content: [{ type: 'text', text: 'Done.' }] } },
  [PI_EVENT.ToolExecutionStart]: { toolCallId: 'call-1', toolName: 'read', args: { path: '/repo/a.ts' } },
  [PI_EVENT.ToolExecutionEnd]: { toolCallId: 'call-1', toolName: 'read', result: { content: [{ type: 'text', text: 'ok' }] } },
  [PI_EVENT.ExtensionUIRequest]: { method: 'notify', message: 'The extension reports this.' },
  [PI_EVENT.EntryAppended]: { entry: { type: 'custom_message', display: true, content: 'A session note.' } },
}

function classify(type: string): string {
  return providerFor(AgentProvider.PI)!.transcript.classify(input({ type, ...PAYLOAD[type] }, undefined, AgentProvider.PI)).kind
}

/** These stored events use ordinary rows instead of the shared notification membership. */
const ROWS: ReadonlyMap<string, string> = new Map([
  [PI_EVENT.MessageEnd, 'assistant_text'],
  [PI_EVENT.EntryAppended, 'assistant_text'],
  [PI_EVENT.ToolExecutionStart, 'tool_use'],
  [PI_EVENT.ToolExecutionEnd, 'tool_result'],
  [PI_EVENT.AgentEnd, 'result_divider'],
  [PI_EVENT.ExtensionUIRequest, 'notification'],
])

describe('pi event vocabulary', () => {
  it.each(Object.values(PI_EVENT))('classifies the stored event %s according to its current reader', (type) => {
    const expected = ROWS.get(type) ?? (PI_NOTIFICATION_EVENT.has(type) ? 'notification' : 'unknown')
    expect(classify(type)).toBe(expected)
  })

  it('leaves an unknown event available for inspection', () => {
    expect(classify('an_event_from_a_later_release')).toBe('unknown')
  })
})
