import { describe, expect, it } from 'vitest'
import { CODEX_CONTROL_TOOL } from '~/generated/contracts/codex-protocol'
import { codexQuestionAnswerRow } from './questionAnswer'

function question(title: unknown) {
  return {
    request: {
      tool_name: CODEX_CONTROL_TOOL.AsyncQuestion,
      input: { title },
    },
  }
}

describe('codexQuestionAnswerRow', () => {
  it('preserves the full title and both source objects', () => {
    const title = `${'Long title '.repeat(100)}\r\nSecond line\n`
    const payload = {
      content: 'Red',
      attachments: [
        { filename: 'sample.png', mime_type: 'image/png' },
      ],
    }
    const source = question(title)
    const before = structuredClone({ payload, source })
    expect(codexQuestionAnswerRow(payload, source)).toEqual({
      kind: 'user',
      text: `> ${title.replace(/\r\n/g, '\n').split('\n').join('\n> ')}\n\nRed`,
      attachments: [
        { filename: 'sample.png', mimeType: 'image/png' },
      ],
    })
    expect({ payload, source }).toEqual(before)
  })

  it('retains the question for an attachment-only answer', () => {
    expect(codexQuestionAnswerRow({
      content: '',
      attachments: [
        { filename: 'choice.txt' },
      ],
    }, question('Which file?'))).toEqual({
      kind: 'user',
      text: '> Which file?\n\n',
      attachments: [
        { filename: 'choice.txt' },
      ],
    })
  })

  it.each([
    null,
    undefined,
    [],
    0,
    {},
    question(null),
    question(' '),
    {
      request: {
        tool_name: 'other',
        input: { title: 'Wrong question' },
      },
    },
  ])('keeps the answer unchanged for an unrelated source %j', (source) => {
    expect(codexQuestionAnswerRow({ content: 'Red' }, source)).toEqual({ kind: 'user', text: 'Red', attachments: [] })
  })

  it('preserves absent and empty user rows', () => {
    expect(codexQuestionAnswerRow(null, question('Which?'))).toBeNull()
    expect(codexQuestionAnswerRow({ content: '' }, question('Which?'))).toEqual({ kind: 'hidden' })
  })
})
