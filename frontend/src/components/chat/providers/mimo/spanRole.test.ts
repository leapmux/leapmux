import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { openingFrame, statusFrame, toolFrame } from '~/test-support/mimoFixtures'
import { input } from '../testUtils'
import { mimoRelatedMessages, mimoSpanRole } from './spanRole'

describe('mimoSpanRole', () => {
  it('reads the opening frame as the request and the final frame as the result', () => {
    expect(mimoSpanRole(input(openingFrame('bash', { command: 'ls' }), undefined, AgentProvider.MIMO_CODE))).toBe('request')
    expect(mimoSpanRole(input(toolFrame('bash', {}), undefined, AgentProvider.MIMO_CODE))).toBe('result')
    expect(mimoSpanRole(input(toolFrame('bash', { status: 'error', error: 'x' }), undefined, AgentProvider.MIMO_CODE))).toBe('result')
  })

  it('reads a retained last frame as the result', () => {
    const parsed = { ...input(openingFrame('bash', { command: 'ls' }), undefined, AgentProvider.MIMO_CODE), completion: MessageCompletion.INTERRUPTED }
    expect(mimoSpanRole(parsed)).toBe('result')
  })

  it('reads any other row as outside a span', () => {
    expect(mimoSpanRole(input(statusFrame('idle'), undefined, AgentProvider.MIMO_CODE))).toBe('other')
    expect(mimoSpanRole(input({ content: 'hi' }, undefined, AgentProvider.MIMO_CODE))).toBe('other')
    expect(mimoSpanRole(input(undefined, undefined, AgentProvider.MIMO_CODE))).toBe('other')
  })

  // A pending frame states no input yet, and the worker never persists one.
  it('reads a pending frame as outside a span', () => {
    expect(mimoSpanRole(input(toolFrame('bash', { status: 'pending' }), undefined, AgentProvider.MIMO_CODE))).toBe('other')
  })
})

describe('mimoRelatedMessages', () => {
  it('pairs each half of a call with the other', () => {
    expect(mimoRelatedMessages(input(openingFrame('bash', { command: 'ls' }), undefined, AgentProvider.MIMO_CODE))).toEqual(['result'])
    expect(mimoRelatedMessages(input(toolFrame('bash', {}), undefined, AgentProvider.MIMO_CODE))).toEqual(['request'])
    expect(mimoRelatedMessages(input(statusFrame('idle'), undefined, AgentProvider.MIMO_CODE))).toEqual([])
  })

  it('pairs a pending frame with nothing', () => {
    expect(mimoRelatedMessages(input(toolFrame('bash', { status: 'pending' }), undefined, AgentProvider.MIMO_CODE))).toEqual([])
  })

  // The last frame of a cut call is the span's result, so it pairs with the request.
  it('pairs a retained last frame with its request', () => {
    const parsed = { ...input(openingFrame('bash', { command: 'ls' }), undefined, AgentProvider.MIMO_CODE), completion: MessageCompletion.INTERRUPTED }
    expect(mimoRelatedMessages(parsed)).toEqual(['request'])
  })
})
