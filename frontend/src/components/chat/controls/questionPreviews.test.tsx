import { fireEvent, render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { ControlRequestContent } from '../ControlRequestBanner'
import { AskUserQuestionContent } from './AskUserQuestionControl'
import { createControlAnswerState } from './types'
import '../providers/claude/plugin'
import '../providers/zcode/plugin'

describe('question option previews', () => {
  it.each([false, true])('keeps the selected response when display details arrive (multiSelect=%s)', (multiSelect) => {
    const answerState = createControlAnswerState()
    const [option, setOption] = createSignal({ label: '1. A — First', value: '1. A — First', preview: '' })
    const request = { requestId: 'question', agentId: 'agent', payload: {} }
    const { getByRole } = render(() => <AskUserQuestionContent request={request} answerState={answerState} questions={[{ question: 'Choose', multiSelect, options: [option()] }]} />)
    fireEvent.click(getByRole(multiSelect ? 'checkbox' : 'radio'))
    setOption({ label: 'A', value: '1. A — First', preview: 'Recovered preview' })
    expect(getByRole(multiSelect ? 'checkbox' : 'radio')).toBeChecked()
    expect(getByRole(multiSelect ? 'checkbox' : 'radio')).toHaveAttribute('value', '1. A — First')
    expect(answerState.selections()[0]).toEqual(['1. A — First'])
    expect(getByRole('region', { name: 'A preview' })).toHaveTextContent('Recovered preview')
  })

  it.each([false, true])('renders a code preview without changing selection (multiSelect=%s)', (multiSelect) => {
    const answerState = createControlAnswerState()
    const { container, getByRole } = render(() => (
      <ControlRequestContent
        agentProvider={AgentProvider.CLAUDE_CODE}
        request={{ requestId: 'preview', agentId: 'agent', payload: { request: { tool_name: 'AskUserQuestion', input: { questions: [{ question: 'Choose an implementation', multiSelect, options: [{ label: 'Simple', description: 'One constant', preview: '```ts\nconst answer = 42\n```' }] }] } } } }}
        answerState={answerState}
      />
    ))
    expect(container.querySelector('pre code')?.textContent).toContain('const answer = 42')
    const preview = getByRole('region', { name: 'Simple preview' })
    expect(getByRole(multiSelect ? 'checkbox' : 'radio')).toHaveAttribute('value', 'Simple')
    fireEvent.click(preview)
    expect(answerState.selections()).toEqual({})
    fireEvent.click(getByRole(multiSelect ? 'checkbox' : 'radio'))
    expect(answerState.selections()[0]).toEqual(['Simple'])
  })

  it('recovers a ZCode preview from native question fields', () => {
    const text = '┌────────┐\n│ sample │\n└────────┘'
    const { getByRole } = render(() => (
      <ControlRequestContent
        agentProvider={AgentProvider.ZCODE}
        request={{ requestId: 'preview', agentId: 'agent', payload: { request: { tool_name: 'AskUserQuestion', input: {} }, params: { questions: [{ question: 'Choose a layout', options: [{ label: 'Box', value: 'Box', preview: text }] }] } } }}
        answerState={createControlAnswerState()}
      />
    ))
    expect(getByRole('region', { name: 'Box preview' }).textContent).toContain(text)
  })

  it.each([undefined, '', '   ', 12, null])('omits an unusable preview (%s)', (preview) => {
    const { queryByRole } = render(() => (
      <ControlRequestContent
        agentProvider={AgentProvider.CLAUDE_CODE}
        request={{ requestId: 'preview', agentId: 'agent', payload: { request: { tool_name: 'AskUserQuestion', input: { questions: [{ question: 'Choose', options: [{ label: 'A', preview }] }] } } } }}
        answerState={createControlAnswerState()}
      />
    ))
    expect(queryByRole('region')).toBeNull()
  })
})
