import type { QuestionIR, QuestionOptionIR } from '../ir/questionBody'
import { describe, expect, it } from 'vitest'
import { pickString } from '~/lib/jsonPick'
import { questionsFromRecords } from './questionRecords'

/** A text reader in the shape a provider passes: the two text fields of one record. */
function readText(record: Record<string, unknown>): Omit<QuestionIR, 'options'> {
  const header = pickString(record, 'header')
  const question = pickString(record, 'question')
  return { ...(header ? { header } : {}), question }
}

/** An option reader in the shape a provider passes: a label, and the sentence beside it. */
function readOption(record: Record<string, unknown>): QuestionOptionIR | null {
  const label = pickString(record, 'label')
  const description = pickString(record, 'description')
  return label ? { label, ...(description ? { description } : {}) } : null
}

describe('questionsFromRecords', () => {
  it('builds one question for each record, with the options the reader kept', () => {
    const questions = questionsFromRecords(
      [{ header: 'Scope', question: 'Which files?', options: [{ label: 'All', description: 'Every file' }, { label: 'Changed' }] }],
      readText,
      readOption,
    )
    expect(questions).toStrictEqual([{
      header: 'Scope',
      question: 'Which files?',
      options: [{ label: 'All', description: 'Every file' }, { label: 'Changed' }],
    }])
  })

  // The first invariant: a question with no text can be neither drawn nor answered,
  // because the row states that text and the control surface keys the answer by it.
  it('drops a record whose text reader answers the empty string', () => {
    const questions = questionsFromRecords(
      [{ header: 'Scope', options: [{ label: 'All' }] }, { question: 'Which files?', options: [] }],
      readText,
      readOption,
    )
    expect(questions).toStrictEqual([{ question: 'Which files?', options: [] }])
  })

  // The second invariant: an option with no label draws an empty button.
  it('drops an option whose reader answers null', () => {
    const questions = questionsFromRecords(
      [{ question: 'Which files?', options: [{ description: 'no label' }, { label: 'All' }] }],
      readText,
      readOption,
    )
    expect(questions).toStrictEqual([{
      question: 'Which files?',
      options: [{ label: 'All' }],
    }])
  })

  it('keeps a question that offers no option at all', () => {
    expect(questionsFromRecords([{ question: 'Which files?' }], readText, readOption))
      .toStrictEqual([{ question: 'Which files?', options: [] }])
  })

  it('reads no option out of an options field that is no array', () => {
    expect(questionsFromRecords([{ question: 'Which files?', options: { label: 'All' } }], readText, readOption))
      .toStrictEqual([{ question: 'Which files?', options: [] }])
  })

  it('skips an entry that is no object, and keeps the ones beside it', () => {
    const questions = questionsFromRecords(
      [null, 'Which files?', ['Which files?'], { question: 'Which files?', options: [] }],
      readText,
      readOption,
    )
    expect(questions).toStrictEqual([{ question: 'Which files?', options: [] }])
  })

  it('skips an option entry that is no object', () => {
    const questions = questionsFromRecords(
      [{ question: 'Which files?', options: [null, 'All', { label: 'All' }] }],
      readText,
      readOption,
    )
    expect(questions).toStrictEqual([{
      question: 'Which files?',
      options: [{ label: 'All' }],
    }])
  })

  it('answers an empty list for a source that is no array', () => {
    expect(questionsFromRecords(undefined, readText, readOption)).toStrictEqual([])
    expect(questionsFromRecords(null, readText, readOption)).toStrictEqual([])
    expect(questionsFromRecords({ questions: [] }, readText, readOption)).toStrictEqual([])
    expect(questionsFromRecords('Which files?', readText, readOption)).toStrictEqual([])
  })

  it('answers an empty list for an empty array', () => {
    expect(questionsFromRecords([], readText, readOption)).toStrictEqual([])
  })

  // The whole record reaches each reader, so a provider spells its own keys there and
  // this module spells none of them.
  it('hands the whole record to each reader', () => {
    const seen: Record<string, unknown>[] = []
    const questions = questionsFromRecords(
      [{ prompt: 'Which files?', options: [{ id: 'all' }] }],
      (record) => {
        seen.push(record)
        return { question: pickString(record, 'prompt') }
      },
      (record) => {
        seen.push(record)
        return { label: pickString(record, 'id') }
      },
    )
    expect(questions).toStrictEqual([{ question: 'Which files?', options: [{ label: 'all' }] }])
    expect(seen).toStrictEqual([{ prompt: 'Which files?', options: [{ id: 'all' }] }, { id: 'all' }])
  })

  // A reader that states no header must leave the key ABSENT rather than present and
  // undefined: several providers compare a built question against a stored one, and
  // `toStrictEqual` and a key count both read the two apart.
  it('carries the header key exactly as the text reader stated it', () => {
    const withKey = questionsFromRecords([{ header: 'Scope', question: 'Which files?' }], readText, readOption)
    const withoutKey = questionsFromRecords([{ header: 'Scope', question: 'Which files?' }], record => ({ question: pickString(record, 'question') }), readOption)
    // Each call answers one question, so each indexed read is the type-level guard alone.
    expect(Object.keys(withKey[0] ?? {}).sort()).toStrictEqual(['header', 'options', 'question'])
    expect(Object.keys(withoutKey[0] ?? {}).sort()).toStrictEqual(['options', 'question'])
  })
})
