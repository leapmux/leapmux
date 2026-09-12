import { render, waitFor } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { prettifyJson } from '~/lib/jsonFormat'
import { testMessageContext } from '~/test-support/messageContext'
import { makeMessage, rawContent } from '~/test-support/messageFactory'
import { createControlAnswerState } from '../../controls/types'
import { OpenCodeControlContent } from '../opencode/OpenCodeControlRequest'
import { ACPControlContent } from './ACPControlRequest'
import '../index'

describe('permission tool details', () => {
  it('formats serialized JSON arguments without changing their provider payload', () => {
    const rawInput = '{"filters":{"limit":0,"enabled":false}}'
    const request = { requestId: 'permission', agentId: 'agent', payload: { params: { toolCall: { title: 'lookup', kind: 'other', rawInput } } } }
    const { container } = render(() => <ACPControlContent request={request} answerState={createControlAnswerState()} />)
    expect(container.querySelector('pre')?.textContent).toBe(prettifyJson(rawInput))
    expect(request.payload.params.toolCall.rawInput).toBe(rawInput)
  })

  it.each([ACPControlContent, OpenCodeControlContent])('shows the command that needs approval', (Content) => {
    const request = { requestId: 'permission', agentId: 'agent', payload: { params: { toolCall: { toolCallId: 'call', title: 'Run checks', kind: 'execute', rawInput: { command: 'npm test -- --runInBand' } } } } }
    const { container } = render(() => <Content request={request} answerState={createControlAnswerState()} />)
    expect(container.textContent).toContain('Run checks')
    expect(container.textContent).toContain('npm test -- --runInBand')
  })

  it('preserves a string patch in the approval details', () => {
    const request = { requestId: 'permission', agentId: 'agent', payload: { params: { toolCall: { title: 'apply_patch', kind: 'edit', rawInput: '*** Begin Patch\n*** Add File: file.ts\n+const value = 42\n*** End Patch' } } } }
    const { container } = render(() => <ACPControlContent request={request} answerState={createControlAnswerState()} />)
    expect(container.textContent).toContain('const value = 42')
  })

  it('loads missing arguments through the shared resolver without changing the permission payload', async () => {
    const request = { requestId: 'permission', agentId: 'agent', payload: { params: { toolCall: { toolCallId: 'call', title: 'Run checks', rawInput: { description: 'Current description' } } } } }
    const original = JSON.stringify(request.payload)
    const message = makeMessage({
      agentProvider: AgentProvider.OPENCODE,
      spanId: 'call',
      content: rawContent({ sessionUpdate: 'tool_call', toolCallId: 'call', kind: 'execute', rawInput: { command: 'npm test -- --runInBand', description: 'Old description' } }),
    })
    const fetchSpan = vi.fn(async () => [message])
    const messageContext = testMessageContext({ fetchSpan })
    const { container, unmount } = render(() => <ACPControlContent request={request} messageContext={messageContext} answerState={createControlAnswerState()} />)
    await waitFor(() => expect(container.textContent).toContain('npm test -- --runInBand'))
    expect(container.textContent).toContain('Current description')
    expect(container.textContent).not.toContain('Old description')
    expect(fetchSpan).toHaveBeenCalledOnce()
    expect(JSON.stringify(request.payload)).toBe(original)
    expect(messageContext.request('call')).toBeDefined()
    unmount()
    expect(messageContext.request('call')).toBeUndefined()
  })

  it.each([
    { scenario: 'empty history', messages: [] },
    { scenario: 'result only', messages: [makeMessage({ spanId: 'call', agentProvider: AgentProvider.OPENCODE, content: rawContent({ sessionUpdate: 'tool_call_update', toolCallId: 'call', status: 'completed' }) })] },
  ])('keeps the provider details when history has no request: $scenario', async ({ messages }) => {
    const request = { requestId: 'permission', agentId: 'agent', payload: { params: { toolCall: { toolCallId: 'call', title: 'Permission from provider', rawInput: { command: 'pwd' } } } } }
    const fetchSpan = vi.fn(async () => messages)
    const messageContext = testMessageContext({ fetchSpan })
    const { container } = render(() => <ACPControlContent request={request} messageContext={messageContext} answerState={createControlAnswerState()} />)
    await messageContext.loadSpan('call')
    expect(container.textContent).toContain('Permission from provider')
    expect(container.textContent).toContain('pwd')
    expect(fetchSpan).toHaveBeenCalledOnce()
  })

  it('keeps the provider details when the history request fails', async () => {
    const error = new Error('Disconnected')
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    const request = { requestId: 'permission', agentId: 'agent', payload: { params: { toolCall: { toolCallId: 'call', title: 'Permission from provider', rawInput: { command: 'pwd' } } } } }
    const fetchSpan = vi.fn(async () => {
      throw error
    })
    const messageContext = testMessageContext({ fetchSpan })
    const { container } = render(() => <ACPControlContent request={request} messageContext={messageContext} answerState={createControlAnswerState()} />)
    await waitFor(() => expect(warn).toHaveBeenCalledWith('Cannot load permission tool details', { id: 'call', error }))
    expect(container.textContent).toContain('Permission from provider')
    expect(container.textContent).toContain('pwd')
    expect(fetchSpan).toHaveBeenCalledOnce()
  })

  it.each([undefined, null, [], { title: { invalid: true }, kind: ['execute'] }])('handles an absent or malformed tool call: %j', (toolCall) => {
    const request = { requestId: 'permission', agentId: 'agent', payload: { params: { toolCall } } }
    const { container } = render(() => <ACPControlContent request={request} answerState={createControlAnswerState()} />)
    expect(container.textContent).toBe('Permission Request')
  })
})
