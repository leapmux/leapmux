import { fireEvent, render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AskUserQuestionContent } from '../../controls/AskUserQuestionControl'
import { createControlAnswerState } from '../../controls/types'
import { getCursorQuestions, sendCursorQuestionResponse } from './CursorControlRequest'

describe('cursor question controls', () => {
  it('keeps separate values for options with the same label', async () => {
    const payload = { params: { questions: [{ id: 'q', prompt: 'Choose', allowMultiple: true, options: [{ id: 'first', label: 'Same' }, { id: 'second', label: 'Same' }] }] } }
    const questions = getCursorQuestions(payload)
    const answerState = createControlAnswerState()
    const request = { requestId: '1', agentId: 'agent', payload }
    const { getAllByRole } = render(() => <AskUserQuestionContent request={request} questions={questions} answerState={answerState} />)
    const options = getAllByRole('checkbox')
    fireEvent.click(options[1])
    expect(options[0]).not.toBeChecked()
    expect(options[1]).toBeChecked()
    const responses: unknown[] = []
    await sendCursorQuestionResponse(async (bytes) => {
      responses.push(JSON.parse(new TextDecoder().decode(bytes)))
    }, '1', questions, answerState)
    expect(responses).toEqual([{ jsonrpc: '2.0', id: 1, result: { outcome: { outcome: 'answered', answers: [{ questionId: 'q', selectedOptionIds: ['second'] }] } } }])
  })

  it('drops malformed questions and options without losing valid choices', () => {
    expect(getCursorQuestions({ params: { questions: [null, 7, { id: 'q', prompt: 'Choose', options: [null, 7, { id: 'valid', label: 'Valid' }] }] } }))
      .toEqual([{ id: 'q', question: 'Choose', header: 'Choose', multiSelect: false, options: [{ value: 'valid', label: 'Valid' }] }])
    expect(getCursorQuestions({ params: { questions: {} } })).toEqual([])
  })
})
