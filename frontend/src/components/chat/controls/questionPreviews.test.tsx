import type { ControlQuestion } from '../model/question'
import type { ControlRequest } from '~/stores/control.store'
import { fireEvent, render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { ControlRequestContent } from '~/test-support/controlRequestBanner'
import { testControlRequest } from '~/test-support/controlRequests'
import { settleWorkerReplies } from '~/test-support/fakeWorker'
import { AskUserQuestionContent } from './AskUserQuestionControl'
import { createControlAnswerState } from './types'

/** A question request, as the Worker sends it for these questions. */
function questionRequest(questions: ControlQuestion[]): ControlRequest {
  return testControlRequest({
    requestId: 'preview',
    agentId: 'agent',
    prompt: { kind: 'question', questions },
  })
}

describe('question option previews', () => {
  it.each([false, true])('keeps the selected response when display details arrive (multiSelect=%s)', async (multiSelect) => {
    const answerState = createControlAnswerState()
    const [option, setOption] = createSignal({ label: '1. A — First', value: '1. A — First', preview: '' })
    const request = testControlRequest({ requestId: 'question', agentId: 'agent' })
    const { getByRole } = render(() => (
      <AskUserQuestionContent
        request={request}
        answerState={answerState}
        preservesSelectionNotes={false}
        questions={[
          {
            question: 'Choose',
            multiSelect,
            options: [
              option(),
            ],
          },
        ]}
      />
    ))
    fireEvent.click(getByRole(multiSelect ? 'checkbox' : 'radio'))
    setOption({ label: 'A', value: '1. A — First', preview: 'Recovered preview' })
    await settleWorkerReplies()
    expect(getByRole(multiSelect ? 'checkbox' : 'radio')).toBeChecked()
    expect(getByRole(multiSelect ? 'checkbox' : 'radio')).toHaveAttribute('value', '1. A — First')
    expect(answerState.selections()[0]).toEqual(['1. A — First'])
    expect(getByRole('region', { name: 'A preview' })).toHaveTextContent('Recovered preview')
  })

  it.each([false, true])('renders a code preview without changing selection (multiSelect=%s)', async (multiSelect) => {
    const answerState = createControlAnswerState()
    const { container, getByRole } = render(() => (
      <ControlRequestContent
        request={questionRequest([
          {
            question: 'Choose an implementation',
            multiSelect,
            options: [
              { label: 'Simple', description: 'One constant', preview: '```ts\nconst answer = 42\n```' },
            ],
          },
        ])}
        answerState={answerState}
      />
    ))
    await settleWorkerReplies()
    expect(container.querySelector('pre code')?.textContent).toContain('const answer = 42')
    const preview = getByRole('region', { name: 'Simple preview' })
    expect(getByRole(multiSelect ? 'checkbox' : 'radio')).toHaveAttribute('value', 'Simple')
    fireEvent.click(preview)
    expect(answerState.selections()).toEqual({})
    fireEvent.click(getByRole(multiSelect ? 'checkbox' : 'radio'))
    expect(answerState.selections()[0]).toEqual(['Simple'])
  })

  it.each([undefined, '', '   '])('omits an empty preview (%j)', (preview) => {
    const { queryByRole } = render(() => (
      <ControlRequestContent
        request={questionRequest([
          {
            question: 'Choose',
            options: [
              {
                label: 'A',
                ...(preview === undefined
                  ? {}
                  : { preview }),
              },
            ],
          },
        ])}
        answerState={createControlAnswerState()}
      />
    ))
    expect(queryByRole('region')).toBeNull()
  })
})
