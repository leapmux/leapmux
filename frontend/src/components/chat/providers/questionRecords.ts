import type { QuestionIR, QuestionOptionIR } from '../ir/questionBody'
import { isObject } from '~/lib/jsonPick'

// The shared builder for the question list a tool call asked.
//
// Four providers -- Cursor, the OpenCode family, ZCode and Pi -- send the same
// structure under different key spellings: a list of question records, each with a
// list of option records. The control flow over that structure is identical, and the
// two invariants inside it are what this module keeps in ONE place:
//
//   - A question with no text is not a question. The row states the text, and the
//     control surface keys the answer by it, so a record that carries none can never
//     be answered or read back.
//   - An option with no label is not an option. The label is the clickable word, so
//     an option without one draws an empty button.
//
// Each provider keeps its own key spellings, in its own plugin, as two readers it
// passes here. That is the split the provider rule states: the shape is neutral and
// shared, the vocabulary is the provider's.

/**
 * The header and the text one provider reads out of a question record.
 *
 * An empty `question` marks a record that asks nothing, and
 * {@link questionsFromRecords} drops it.
 */
export type QuestionTextReader = (record: Record<string, unknown>) => Omit<QuestionIR, 'options'>

/**
 * One option in the shared shape, or null for a record that offers nothing to click.
 *
 * The reader answers null rather than an empty label, so the drop decision stays with
 * the provider that knows which of its fields can stand in for a missing one.
 */
export type QuestionOptionReader = (record: Record<string, unknown>) => QuestionOptionIR | null

/**
 * The questions a call asked, from the raw records it carries.
 *
 * `source` is the raw value of the provider's own question list: anything that is not
 * an array carries no question, and a non-object entry inside one carries none either.
 * The options come from the `options` key, which all four providers spell that way.
 */
export function questionsFromRecords(
  source: unknown,
  readText: QuestionTextReader,
  readOption: QuestionOptionReader,
): QuestionIR[] {
  const records = Array.isArray(source) ? source.filter(isObject) : []
  return records.flatMap((record) => {
    const text = readText(record)
    if (!text.question)
      return []
    const options = Array.isArray(record.options) ? record.options.filter(isObject) : []
    return [{
      ...text,
      options: options.flatMap((option) => {
        const built = readOption(option)
        return built ? [built] : []
      }),
    }]
  })
}
