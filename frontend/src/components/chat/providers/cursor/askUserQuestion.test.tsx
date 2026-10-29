import { fireEvent, render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AskUserQuestionContent } from '../../controls/AskUserQuestionControl'
import { createControlAnswerState } from '../../controls/types'
import { getCursorQuestions, sendCursorQuestionRejectResponse, sendCursorQuestionResponse } from './askUserQuestion'

describe('cursor question controls', () => {
  it('declares the native ACP limit on custom answers', () => {
    const questions = getCursorQuestions({
      params: {
        questions: [
          {
            id: 'q',
            prompt: 'Choose',
            options: [
              { id: 'first', label: 'First' },
            ],
          },
        ],
      },
    })
    expect(questions).toMatchObject([
      { allowCustomText: false },
    ])
  })

  it('refuses a stale custom answer before the native ACP adapter can discard it', async () => {
    const questions = getCursorQuestions({
      params: {
        questions: [
          {
            id: 'q',
            prompt: 'Choose',
            options: [
              { id: 'first', label: 'First' },
            ],
          },
        ],
      },
    })
    const state = createControlAnswerState({
      customTexts: { 0: 'Keep this answer.' },
    })
    const send = vi.fn().mockResolvedValue(undefined)
    await expect(sendCursorQuestionResponse(send, '1', questions, state)).rejects.toThrow('selected options only')
    expect(send).not.toHaveBeenCalled()
  })

  it('keeps separate values for options with the same label', async () => {
    const payload = {
      params: {
        questions: [
          {
            id: 'q',
            prompt: 'Choose',
            allowMultiple: true,
            options: [
              { id: 'first', label: 'Same' },
              { id: 'second', label: 'Same' },
            ],
          },
        ],
      },
    }
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
    expect(responses).toEqual([
      {
        jsonrpc: '2.0',
        id: '1',
        result: {
          outcome: {
            outcome: 'answered',
            answers: [
              {
                questionId: 'q',
                selectedOptionIds: ['second'],
              },
            ],
          },
        },
      },
    ])
  })

  for (const selected of [false, true]) {
    it(`refuses custom text when a native option is selected: ${selected}`, async () => {
      const questions = getCursorQuestions({
        params: {
          questions: [
            {
              id: 'q',
              prompt: 'Choose',
              options: [
                { id: 'first', label: 'First' },
              ],
            },
          ],
        },
      })
      const state = createControlAnswerState({
        selections: selected
          ? {
              0: ['first'],
            }
          : {},
        customTexts: { 0: '  a reason of my own  ' },
      })
      const respond = vi.fn().mockResolvedValue(undefined)
      await expect(sendCursorQuestionResponse(respond, '1', questions, state)).rejects.toThrow('selected options only')
      expect(respond).not.toHaveBeenCalled()
      expect(state.customTexts()[0]).toBe('  a reason of my own  ')
    })
  }

  it('states no typed answer when the reader gave none', async () => {
    const payload = {
      params: {
        questions: [
          {
            id: 'q',
            prompt: 'Choose',
            options: [
              { id: 'first', label: 'First' },
            ],
          },
        ],
      },
    }
    const questions = getCursorQuestions(payload)
    const answerState = createControlAnswerState()
    answerState.setSelections({
      0: ['first'],
    })
    const responses: Array<Record<string, any>> = []
    await sendCursorQuestionResponse(async (bytes) => {
      responses.push(JSON.parse(new TextDecoder().decode(bytes)))
    }, '1', questions, answerState)
    expect(responses[0]?.result.outcome.answers).toEqual([
      {
        questionId: 'q',
        selectedOptionIds: ['first'],
      },
    ])
  })

  it('drops malformed questions and options without losing valid choices', () => {
    expect(getCursorQuestions({
      params: {
        questions: [
          null,
          7,
          {
            id: 'q',
            prompt: 'Choose',
            options: [
              null,
              7,
              { id: 'valid', label: 'Valid' },
            ],
          },
        ],
      },
    }))
      .toEqual([
        {
          id: 'q',
          question: 'Choose',
          header: 'Choose',
          multiSelect: false,
          allowCustomText: false,
          options: [
            { value: 'valid', label: 'Valid' },
          ],
        },
      ])
    expect(getCursorQuestions({
      params: { questions: {} },
    })).toEqual([])
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
    expect(await sent('changed mind')).toEqual([
      {
        jsonrpc: '2.0',
        id: 'jsonrpc:4',
        result: {
          outcome: { outcome: 'cancelled', reason: 'changed mind' },
        },
      },
    ])
  })

  it('states no reason field for an empty or absent reason', async () => {
    expect(await sent('')).toEqual([
      {
        jsonrpc: '2.0',
        id: 'jsonrpc:4',
        result: {
          outcome: { outcome: 'cancelled' },
        },
      },
    ])
    expect(await sent()).toEqual([
      {
        jsonrpc: '2.0',
        id: 'jsonrpc:4',
        result: {
          outcome: { outcome: 'cancelled' },
        },
      },
    ])
  })
})
