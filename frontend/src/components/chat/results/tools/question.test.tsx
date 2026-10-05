import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { checkKindModule } from '~/test-support/kindTestHarness'
import { toolCallFixture, toolRow } from '~/test-support/toolCallFixture'
import { ToolMessage } from '../ToolMessage'
import { questionRenderer } from './question'
import { parsedCall } from './renderer'

describe('question renderer', () => {
  checkKindModule({
    kind: 'question',
    request: { questions: [{ header: 'Deploy', question: 'Which env?', options: [{ label: 'Staging' }] }] },
    // The SENTENCE heads the row. `header` is the short caption that tells several
    // questions apart, and it is deliberately not the title.
    titlePart: 'Which env?',
    result: { answers: [{ header: 'Deploy', answer: 'Staging' }] },
    resultPart: 'Deploy',
  })

  /** One title, composed from the request the way every other kind composes its own. */
  const titleOf = (questions: Array<{ header?: string, question: string }>, title?: string): string =>
    questionRenderer.title(parsedCall(toolCallFixture('question', {
      request: { questions: questions.map(entry => ({ ...entry, options: [] })) },
      ...(title === undefined ? {} : { title }),
    })), undefined) as string

  // The extractor computes a sentence for every question. Preferring the twelve-
  // character header threw that sentence away for each question that carried one.
  it('heads one question with its sentence, not its caption', () => {
    expect(titleOf([{ header: 'Deploy', question: 'Which environment should the build reach?' }]))
      .toBe('Which environment should the build reach?')
  })

  it('marks the answers as returned output and leaves the question unmarked', () => {
    const request = { questions: [{ header: 'Deploy', question: 'Which env?', options: [{ label: 'Staging' }] }] }
    const answered = render(() => <ToolMessage row={toolRow(toolCallFixture('question', { request, result: { answers: [{ header: 'Deploy', answer: 'Staging' }] } }))} />)
    const outputs = answered.container.querySelectorAll('[data-tool-output-preview]')
    expect(outputs).toHaveLength(1)
    expect(outputs[0]?.textContent).toContain('Staging')
    expect(outputs[0]?.textContent).not.toContain('Which env?')
    const asking = render(() => <ToolMessage row={toolRow(toolCallFixture('question', { status: 'in_progress', request }), 'request')} />)
    expect(asking.container.textContent).toContain('Which env?')
    expect(asking.container.querySelector('[data-tool-output-preview]')).toBeNull()
  })

  it('heads several questions with their count', () => {
    expect(titleOf([{ question: 'One?' }, { question: 'Two?' }])).toBe('2 questions')
  })

  // `question` is required, so a provider that carries no sentence states an empty
  // one. A blank header would be unreadable, and no later step could replace it.
  it('falls through an empty sentence to the call\'s own title', () => {
    expect(titleOf([{ header: 'Deploy', question: '' }], 'Pick a target')).toBe('Pick a target')
    expect(titleOf([{ question: '' }])).toBe('Question')
    expect(titleOf([])).toBe('Question')
  })
})
