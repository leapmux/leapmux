import type { QuestionOption, QuestionPrompt } from '../model/question'

/** Indent every line, so a multi-line preview stays inside its own list item. */
function indent(text: string, by: string): string {
  return text.split('\n').map(line => (line ? by + line : line)).join('\n')
}

/** One option: its label, the sentence that tells it apart, then its worked example. */
function optionMarkdown(option: QuestionOption): string {
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
 * it. Each provider maps its own wire record onto {@link QuestionPrompt} in its own
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
export function questionBodyMarkdown(questions: QuestionPrompt[]): string {
  return questions
    .map((entry) => {
      const options = entry.options.map(optionMarkdown).join('\n')
      return [entry.question, options].filter(Boolean).join('\n\n')
    })
    .filter(Boolean)
    .join('\n\n')
}
