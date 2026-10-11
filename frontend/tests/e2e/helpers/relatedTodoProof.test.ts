import { describe, expect, it } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { piTodoToolCall, updateTodosToolCall } from './providerToolCalls'
import { RELATED_TODO_CALL_ID, RELATED_TODO_ITEM, relatedTodoTurn } from './relatedTodoProof'

describe('relatedTodoTurn', () => {
  it.each([AgentProvider.OPENCODE, AgentProvider.GOOSE, AgentProvider.ZCODE])('builds the update-todos call of provider %s for the default item', (provider) => {
    const turn = relatedTodoTurn(provider)
    expect(turn.item).toBe(RELATED_TODO_ITEM)
    expect(turn.steps).toEqual([
      { toolCalls: [updateTodosToolCall(provider, RELATED_TODO_CALL_ID, [{ step: RELATED_TODO_ITEM, status: 'pending' }])] },
      { text: 'The native sidebar capability proof ended.' },
    ])
  })

  it('answers in the step of the tool call for a provider that runs the tool in one exchange', () => {
    const turn = relatedTodoTurn(AgentProvider.CURSOR, { answerStep: 'same-step' })
    expect(turn.steps).toEqual([{
      toolCalls: [updateTodosToolCall(AgentProvider.CURSOR, RELATED_TODO_CALL_ID, [{ step: RELATED_TODO_ITEM, status: 'pending' }])],
      text: 'The native sidebar capability proof ended.',
    }])
  })

  it('creates the item that the sidebar check requires when the caller gives another item', () => {
    const turn = relatedTodoTurn(AgentProvider.KILO, { item: 'Another native item' })
    expect(turn.item).toBe('Another native item')
    expect(turn.steps[0]).toEqual({ toolCalls: [updateTodosToolCall(AgentProvider.KILO, RELATED_TODO_CALL_ID, [{ step: 'Another native item', status: 'pending' }])] })
  })

  it('uses the call of a provider that has no update-todos call, and keeps the default item', () => {
    const toolCall = piTodoToolCall(RELATED_TODO_CALL_ID, { action: 'create', subject: RELATED_TODO_ITEM })
    const turn = relatedTodoTurn(AgentProvider.PI, { toolCall })
    expect(turn.item).toBe(RELATED_TODO_ITEM)
    expect(turn.steps[0]).toEqual({ toolCalls: [toolCall] })
  })

  // Pi's vocabulary has no update-todos call, so its specs must pass their own call.
  it('refuses the default call for a provider whose vocabulary has no update-todos call', () => {
    expect(() => relatedTodoTurn(AgentProvider.PI)).toThrow(`AgentProvider ${AgentProvider.PI} has no`)
  })

  it.each(['', ' '])('refuses an item with no text: %j', (item) => {
    expect(() => relatedTodoTurn(AgentProvider.KILO, { item })).toThrow('needs text')
  })
})
