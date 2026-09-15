import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { input } from '../testUtils'
import { piQuestionFromSource } from './questionSource'

const fixture = JSON.parse(readFileSync('../testdata/pi_question_source_conformance.json', 'utf8')) as {
  cases: Array<{ name: string, dialog: Record<string, unknown>, args: unknown, expectedIndex: number | null }>
}

describe('pi question source conformance', () => {
  it.each(fixture.cases)('$name', (item) => {
    const source = input({ type: 'tool_execution_start', toolName: 'ask_user_question', toolCallId: 'question', args: item.args })
    const before = JSON.stringify(source)
    expect(piQuestionFromSource(item.dialog, source)?.index ?? null).toBe(item.expectedIndex)
    expect(JSON.stringify(source)).toBe(before)
  })
})
