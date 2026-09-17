/**
 * One choice a question offered.
 *
 * `preview` is the option's own worked example -- a mockup, a snippet, the shape the
 * answer would produce. The control surface draws it in a region of its own
 * (`QuestionOptionItem`), and the transcript row must draw it too: a reader who
 * comes back to the row sees the choice they made and has to be able to tell what
 * the alternatives actually were.
 */
export interface QuestionOptionIR {
  label: string
  description?: string
  preview?: string
}

/**
 * One question a tool asked, in the shape every provider's own record maps onto.
 *
 * `header` is the short label a question carries beside its text -- Claude caps it
 * at twelve characters, and ZCode and the OpenCode family send the same field. It
 * identifies the CHOICE rather than repeating the question, which is what lets a row that
 * asked four questions tell them apart on one line each.
 */
export interface QuestionIR {
  header?: string
  question: string
  options: QuestionOptionIR[]
}

/** Indent every line, so a multi-line preview stays inside its own list item. */
function indent(text: string, by: string): string {
  return text.split('\n').map(line => (line ? by + line : line)).join('\n')
}

/** One option: its label, the sentence that tells it apart, then its worked example. */
function optionMarkdown(option: QuestionOptionIR): string {
  const head = option.description ? `**${option.label}** — ${option.description}` : `**${option.label}**`
  const preview = option.preview?.trim()
  // Two spaces of indent is what keeps a fenced block inside the bullet. A preview
  // often IS a fenced block already, so it is never fenced a second time here --
  // that would show the reader the backticks instead of the code.
  return preview ? `- ${head}\n\n${indent(preview, '  ')}` : `- ${head}`
}

/**
 * The questions a tool asked, as the Markdown its row draws.
 *
 * ONE builder for every provider, because a question reads the same whoever asked
 * it. Each provider maps its own wire record onto {@link QuestionIR} in its own
 * plugin -- the field a provider calls `prompt` and another calls `question` is that
 * provider's spelling, and it stays there.
 *
 * A single question states its text and its options. Several put each question's
 * own text above its own options, because a flat list of labels from four questions
 * says nothing about which question each label belongs to.
 *
 * Returns the empty string when nothing is left to draw, so the row falls back to
 * its plain body rather than drawing an empty list.
 */
export function questionBodyMarkdown(questions: QuestionIR[]): string {
  return questions
    .map((entry) => {
      const options = entry.options.map(optionMarkdown).join('\n')
      return [entry.question, options].filter(Boolean).join('\n\n')
    })
    .filter(Boolean)
    .join('\n\n')
}
