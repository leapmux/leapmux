/**
 * Refuse an input that holds no text, before a rule or a reader matches on it. An empty or blank value would match
 * every request, so the rule would prove nothing. `subject` states what needs the text, as in
 * `The Pi notification`, and `field` gives the input.
 */
export function requireNonemptyText(value: unknown, subject: string, field: string): asserts value is string {
  if (typeof value !== 'string' || value.trim() === '')
    throw new Error(`${subject} requires nonempty text for ${field}.`)
}
