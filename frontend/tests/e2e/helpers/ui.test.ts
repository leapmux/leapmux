import { describe, expect, it } from 'vitest'
import {
  ARITHMETIC_ANSWER,
  ARITHMETIC_ANSWER_TEXT,
  ARITHMETIC_PROMPT,
  SECOND_ARITHMETIC_ANSWER,
  SECOND_ARITHMETIC_ANSWER_TEXT,
  SECOND_ARITHMETIC_PROMPT,
} from './ui'

describe('ARITHMETIC_ANSWER', () => {
  it('matches the literal a scenario returns', () => {
    expect(ARITHMETIC_ANSWER.test(ARITHMETIC_ANSWER_TEXT)).toBe(true)
    expect(SECOND_ARITHMETIC_ANSWER.test(SECOND_ARITHMETIC_ANSWER_TEXT)).toBe(true)
  })

  it('states the arithmetic each prompt asks for', () => {
    expect(String(1234 + 5678)).toBe(ARITHMETIC_ANSWER_TEXT)
    expect(ARITHMETIC_PROMPT).toContain('1234 + 5678')
    expect(String(1111 + 2222)).toBe(SECOND_ARITHMETIC_ANSWER_TEXT)
    expect(SECOND_ARITHMETIC_PROMPT).toContain('1111 + 2222')
  })

  it('keeps the two answers from satisfying each other', () => {
    expect(ARITHMETIC_ANSWER.test(SECOND_ARITHMETIC_ANSWER_TEXT)).toBe(false)
    expect(SECOND_ARITHMETIC_ANSWER.test(ARITHMETIC_ANSWER_TEXT)).toBe(false)
  })
})
