import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import { diracNotificationEntry } from './notification'
import './plugin'

function chunk(text: string): Record<string, unknown> {
  return {
    sessionUpdate: 'agent_message_chunk',
    content: { type: 'text', text },
  }
}

describe('diracNotificationEntry', () => {
  const plugin = providerFor(AgentProvider.DIRAC)!

  it('reads the retry notice as a status row', () => {
    const frame = chunk('Retrying API request...')
    expect(plugin.transcript.notificationEntry?.(frame)).toEqual([
      { kind: 'status', text: 'Retrying the model request' },
    ])
    expect(plugin.transcript.classify(input(frame, undefined, AgentProvider.DIRAC)).kind).toBe('notification')
  })

  it('reads a cancelled hook notice as a blocked hook', () => {
    expect(plugin.transcript.notificationEntry?.(chunk('\nHook execution cancelled by user'))).toEqual([
      { kind: 'hook', outcome: 'blocked', detail: 'cancelled by user' },
    ])
  })

  it('reads the shell-integration lines as status rows', () => {
    expect(diracNotificationEntry(chunk('Shell integration is not available.')).length).toBe(1)
    expect(diracNotificationEntry(chunk('Shell integration is not available. Consider using background execution mode for better performance.')).length).toBe(1)
  })

  it('leaves model chunks and other frames unknown', () => {
    expect(diracNotificationEntry(chunk('A model answer.'))).toEqual([])
    expect(diracNotificationEntry({ sessionUpdate: 'tool_call' })).toEqual([])
  })
})
