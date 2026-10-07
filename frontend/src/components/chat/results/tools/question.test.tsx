import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { checkKindModule } from '~/test-support/kindTestHarness'
import { toolCallFixture, toolRow } from '~/test-support/toolCallFixture'
import { ToolMessage } from '../ToolMessage'
import { questionRenderer } from './question'
import { parsedCall, resolvedCall } from './renderer'

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

  // A result row can stand alone in the transcript, beside the request row of its
  // call. A dismissal states no answer, so the provider's own note is all it can draw:
  // a row that drew nothing measured zero height, and the transcript held every later
  // row hidden behind it.
  it('draws the note of a result that states no answer', () => {
    const request = { questions: [{ header: 'Color', question: 'Which color?', options: [{ label: 'Blue' }] }] }
    const result = { answers: [], note: 'User dismissed the question without answering.' }
    const dismissed = render(() => <ToolMessage row={toolRow(toolCallFixture('question', { request, result }))} />)
    const outputs = dismissed.container.querySelectorAll('[data-tool-output-preview]')
    expect(outputs).toHaveLength(1)
    expect(outputs[0]?.textContent).toContain('User dismissed the question without answering.')
    const resolved = resolvedCall(toolCallFixture('question', { request, result }))
    if (!resolved)
      throw new Error('The dismissed question carries no result of its own kind.')
    expect(questionRenderer.resultMeta(resolved).copyableContent?.()).toBe('User dismissed the question without answering.')
  })

  it('draws the answers before the note of a result that states both', () => {
    const request = { questions: [{ header: 'Color', question: 'Which color?', options: [{ label: 'Blue' }] }] }
    const result = { answers: [{ header: 'Color', answer: 'Blue' }], note: 'The reader typed nothing else.' }
    const answered = render(() => <ToolMessage row={toolRow(toolCallFixture('question', { request, result }))} />)
    const output = answered.container.querySelector('[data-tool-output-preview]')?.textContent ?? ''
    expect(output.indexOf('Blue')).toBeGreaterThanOrEqual(0)
    expect(output.indexOf('Blue')).toBeLessThan(output.indexOf('The reader typed nothing else.'))
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
