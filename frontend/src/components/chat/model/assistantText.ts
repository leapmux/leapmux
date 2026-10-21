/**
 * One segment of an assistant text row that a provider splits itself.
 *
 * `text` is ordinary markdown. `steer-note` marks a note the model echoed
 * about a steer the reader sent mid-turn: the note is not the model's own
 * prose, so the renderer draws it as a chip instead of markdown, with the
 * steer's own identity attached.
 */
export type AssistantTextSegment
  = | { kind: 'text', text: string }
    | { kind: 'steer-note', steerId: string, note: string }
