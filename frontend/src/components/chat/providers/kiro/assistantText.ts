import type { AssistantTextSegment } from '../../model/assistantText'

/**
 * Kiro echoes a note about each steer the reader sent mid-turn inside the reply
 * itself, as `[STEERING steer-<id>: note]`. The note is Kiro's bookkeeping, not
 * the model's prose, so this hook splits the reply into text segments and steer
 * notes; the renderer draws each note as a chip with its steer id.
 */
const STEER_NOTE = /\[STEERING (steer-[A-Za-z0-9-]+): ([^\]\n]*)\]/g

export function kiroAssistantTextSegments(text: string): AssistantTextSegment[] | undefined {
  if (!text.includes('[STEERING '))
    return undefined
  const matches = [...text.matchAll(STEER_NOTE)]
  if (matches.length === 0)
    return undefined
  const segments: AssistantTextSegment[] = []
  let last = 0
  for (const match of matches) {
    const index = match.index ?? 0
    const before = text.slice(last, index)
    if (before !== '')
      segments.push({ kind: 'text', text: before })
    const steerId = match[1] ?? ''
    const note = match[2] ?? ''
    const full = match[0] ?? ''
    segments.push({ kind: 'steer-note', steerId, note })
    last = index + full.length
  }
  const tail = text.slice(last)
  if (tail !== '')
    segments.push({ kind: 'text', text: tail })
  return segments
}
