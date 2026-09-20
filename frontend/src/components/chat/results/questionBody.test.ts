import { describe, expect, it } from 'vitest'
import { questionBodyMarkdown } from './questionBody'

describe('questionBodyMarkdown', () => {
  it('states the question and then its choices', () => {
    expect(questionBodyMarkdown([{
      question: 'Which parser?',
      options: [{ label: 'Recursive descent' }, { label: 'PEG' }],
    }])).toBe('Which parser?\n\n- **Recursive descent**\n- **PEG**')
  })

  // The sentence beside a label is what tells two similar choices apart, and the
  // control banner shows it. A row that dropped it left the reader with two words.
  it('keeps the sentence that tells two choices apart', () => {
    expect(questionBodyMarkdown([{
      question: 'Which parser?',
      options: [{ label: 'PEG', description: 'One grammar, no separate lexer.' }],
    }])).toBe('Which parser?\n\n- **PEG** — One grammar, no separate lexer.')
  })

  // A preview is the option's own worked example. The control surface draws it in a
  // region of its own, so the row has to draw it too -- a reader who comes back to
  // the row must be able to tell what the alternatives actually were.
  it('draws an option preview under its own choice', () => {
    expect(questionBodyMarkdown([{
      question: 'Which shape?',
      options: [{ label: 'Simple', preview: '```ts\nconst answer = 42\n```' }],
    }])).toBe('Which shape?\n\n- **Simple**\n\n  ```ts\n  const answer = 42\n  ```')
  })

  // Two spaces of indent is what keeps the fenced block INSIDE the bullet. Without
  // it the code would close the list and read as the next question's body.
  it('indents every line of a preview, and leaves its blank lines bare', () => {
    expect(questionBodyMarkdown([{
      question: 'Which?',
      options: [{ label: 'A', preview: 'first\n\nsecond' }],
    }])).toBe('Which?\n\n- **A**\n\n  first\n\n  second')
  })

  // A preview that is already fenced is NEVER fenced a second time: that would show
  // the reader the backticks instead of the code.
  it('does not fence a preview a second time', () => {
    const text = questionBodyMarkdown([{ question: 'Q', options: [{ label: 'A', preview: '```sh\nls\n```' }] }])
    expect(text.match(/```/g)).toHaveLength(2)
  })

  // Several questions each state their own text, because a flat list of labels from
  // four questions says nothing about which question each label belongs to.
  it('keeps each question with its own choices', () => {
    expect(questionBodyMarkdown([
      { question: 'First?', options: [{ label: 'A' }] },
      { question: 'Second?', options: [{ label: 'B' }] },
    ])).toBe('First?\n\n- **A**\n\nSecond?\n\n- **B**')
  })

  it('states a question that offered no choice', () => {
    expect(questionBodyMarkdown([{ question: 'What should I build?', options: [] }]))
      .toBe('What should I build?')
  })

  // The empty string is what lets a row fall back to its plain body instead of
  // drawing an empty list.
  it('answers nothing for no question at all', () => {
    expect(questionBodyMarkdown([])).toBe('')
  })

  // A whitespace-only preview is not a preview. The control surface applies the same
  // rule, so the two surfaces show the same option.
  it('omits a preview that holds only whitespace', () => {
    expect(questionBodyMarkdown([{ question: 'Q', options: [{ label: 'A', preview: '   \n\t ' }] }]))
      .toBe('Q\n\n- **A**')
  })
})
