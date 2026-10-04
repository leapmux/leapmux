import { fireEvent, render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AskUserQuestionContent } from '../../controls/AskUserQuestionControl'
import { createControlAnswerState } from '../../controls/types'
import { getCursorQuestions, sendCursorQuestionRejectResponse, sendCursorQuestionResponse } from './askUserQuestion'

describe('cursor question controls', () => {
  it('keeps separate values for options with the same label', async () => {
    const payload = { params: { questions: [{ id: 'q', prompt: 'Choose', allowMultiple: true, options: [{ id: 'first', label: 'Same' }, { id: 'second', label: 'Same' }] }] } }
    const questions = getCursorQuestions(payload)
    const answerState = createControlAnswerState()
    const request = { requestId: '1', agentId: 'agent', payload }
    const { getAllByRole } = render(() => <AskUserQuestionContent request={request} questions={questions} answerState={answerState} />)
    const options = getAllByRole('checkbox')
    // The payload above renders exactly two checkboxes; the fallback is the type-level guard alone.
    fireEvent.click(options[1] ?? document.body)
    expect(options[0]).not.toBeChecked()
    expect(options[1]).toBeChecked()
    const responses: unknown[] = []
    await sendCursorQuestionResponse(async (bytes) => {
      responses.push(JSON.parse(new TextDecoder().decode(bytes)))
    }, '1', questions, answerState)
    expect(responses).toEqual([{ jsonrpc: '2.0', id: '1', result: { outcome: { outcome: 'answered', answers: [{ questionId: 'q', selectedOptionIds: ['second'] }] } } }])
  })

  // Cursor's own answer carries `freeformText` beside `selectedOptionIds`, and its TUI
  // reads both. Dropping the typed text loses an answer the reader gave. See RL-006.
  it('carries a typed answer beside the selected options', async () => {
    const payload = { params: { questions: [{ id: 'q', prompt: 'Choose', options: [{ id: 'first', label: 'First' }] }] } }
    const questions = getCursorQuestions(payload)
    const answerState = createControlAnswerState()
    answerState.setSelections({ 0: ['first'] })
    answerState.setCustomTexts({ 0: '  a reason of my own  ' })
    const responses: unknown[] = []
    await sendCursorQuestionResponse(async (bytes) => {
      responses.push(JSON.parse(new TextDecoder().decode(bytes)))
    }, '1', questions, answerState)
    expect(responses).toEqual([{ jsonrpc: '2.0', id: '1', result: { outcome: { outcome: 'answered', answers: [
      { questionId: 'q', selectedOptionIds: ['first'], freeformText: 'a reason of my own' },
    ] } } }])
  })

  // A question answered with typed text ALONE is still an answer.
  it('sends a typed answer for a question with no option selected', async () => {
    const payload = { params: { questions: [{ id: 'q', prompt: 'Choose', options: [{ id: 'first', label: 'First' }] }] } }
    const questions = getCursorQuestions(payload)
    const answerState = createControlAnswerState()
    answerState.setCustomTexts({ 0: 'neither of those' })
    const responses: Array<Record<string, any>> = []
    await sendCursorQuestionResponse(async (bytes) => {
      responses.push(JSON.parse(new TextDecoder().decode(bytes)))
    }, '1', questions, answerState)
    expect(responses[0]?.result.outcome.answers).toEqual([
      { questionId: 'q', selectedOptionIds: [], freeformText: 'neither of those' },
    ])
  })

  it('states no typed answer when the reader gave none', async () => {
    const payload = { params: { questions: [{ id: 'q', prompt: 'Choose', options: [{ id: 'first', label: 'First' }] }] } }
    const questions = getCursorQuestions(payload)
    const answerState = createControlAnswerState()
    answerState.setSelections({ 0: ['first'] })
    const responses: Array<Record<string, any>> = []
    await sendCursorQuestionResponse(async (bytes) => {
      responses.push(JSON.parse(new TextDecoder().decode(bytes)))
    }, '1', questions, answerState)
    expect(responses[0]?.result.outcome.answers).toEqual([{ questionId: 'q', selectedOptionIds: ['first'] }])
  })

  it('drops malformed questions and options without losing valid choices', () => {
    expect(getCursorQuestions({ params: { questions: [null, 7, { id: 'q', prompt: 'Choose', options: [null, 7, { id: 'valid', label: 'Valid' }] }] } }))
      .toEqual([{ id: 'q', question: 'Choose', header: 'Choose', multiSelect: false, options: [{ value: 'valid', label: 'Valid' }] }])
    expect(getCursorQuestions({ params: { questions: {} } })).toEqual([])
  })
})

// Cursor reads a dismissed question as the protocol's `cancelled` outcome, and the
// reason the reader typed rides beside it. The worker stores the same word, so the
// saved row reads it back (see controlResponse.test.ts).
describe('sendCursorQuestionRejectResponse', () => {
  async function sent(reason?: string): Promise<unknown[]> {
    const responses: unknown[] = []
    await sendCursorQuestionRejectResponse(async (bytes) => {
      responses.push(JSON.parse(new TextDecoder().decode(bytes)))
    }, 'jsonrpc:4', reason)
    return responses
  }

  it('cancels the question with the reason the reader typed', async () => {
    expect(await sent('changed mind')).toEqual([{ jsonrpc: '2.0', id: 'jsonrpc:4', result: { outcome: { outcome: 'cancelled', reason: 'changed mind' } } }])
  })

  it('states no reason field for an empty or absent reason', async () => {
    expect(await sent('')).toEqual([{ jsonrpc: '2.0', id: 'jsonrpc:4', result: { outcome: { outcome: 'cancelled' } } }])
    expect(await sent()).toEqual([{ jsonrpc: '2.0', id: 'jsonrpc:4', result: { outcome: { outcome: 'cancelled' } } }])
  })
})
