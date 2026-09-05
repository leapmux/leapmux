import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { collectE2EFiles } from '~/test-support/e2eFiles'
import { frontendRoot, posixRelative } from '~/test-support/sourceTree'

// E2E guard: read a chat row through `readAttached`, never through a bare
// `locator.evaluate` / `locator.evaluateAll`.
//
// Resolving a locator and reading from what it resolved are two browser round
// trips, and a chat row does not always survive the gap. ChatView re-creates a
// row when a notification changes its sequence. A resolve just before the swap leaves the read
// holding a DETACHED node.
//
// `evaluateAll` is no safer than `evaluate`, which is worth stating because it
// reads as though it should be: Playwright runs it as `querySelectorAll` into an
// array HANDLE and then a second call that applies the callback to that handle,
// so the elements are pinned a round trip before the callback sees them. A probe
// that replaced its element on every event-loop turn fed both forms a detached
// node on most attempts.
//
// What makes this worth a guard rather than a comment is how quietly it fails. A
// detached node reports an empty rect, a null `closest()` and NO computed style,
// so a geometry assertion fails at random (issue 402) while a colour assertion of
// the "these two must differ" shape PASSES against an empty string -- green, and
// proving nothing. 033 held one of those.
//
// `readAttached` in tests/e2e/helpers/ui.ts takes the read that the guard
// requires: it hands the callback every match, the callback keeps only what is
// still in the document, and returning null retries.
//
// Only CHAT locators are in scope. The scroll container, the composer, a popover
// and the sidebar are not rebuilt under a reader, so `locator.evaluate` stays
// correct for them.

/** What marks an expression as resolving to a chat row, or to something inside one. */
const CHAT_LOCATOR_MARKERS = [
  'userBubbles(',
  'assistantBubbles(',
  'messageBubbles(',
  'messageContents(',
  'bandRows(',
  'firstAssistantBubble(',
  'lastAssistantBubble(',
  'message-bubble',
  'message-content',
  'data-seq',
  'data-band',
]

/** The two-round-trip reads, as they appear after a receiver. */
const RACY_READ = String.raw`\.\s*evaluate(?:All)?\s*\(`

/** `const name = <expression>`, the only binding form the specs use for a locator. */
const BINDING = /(?:const|let)\s+(\w+)\s*=\s*([^\n]*)/g

/**
 * The names in `source` that hold a chat locator.
 *
 * Runs to a fixed point so a locator scoped off another one is caught too:
 * `const sent = bubble.locator('pre')` is as exposed to its row's remount as
 * `bubble` is.
 */
function chatLocatorNames(source: string): Set<string> {
  const names = new Set<string>()
  for (let grew = true; grew;) {
    grew = false
    for (const [, name, expression] of source.matchAll(BINDING)) {
      if (names.has(name))
        continue
      const derived = [...names].some(known => new RegExp(`\\b${known}\\b`).test(expression))
      if (!CHAT_LOCATOR_MARKERS.some(marker => expression.includes(marker)) && !derived)
        continue
      names.add(name)
      grew = true
    }
  }
  return names
}

/** Every two-round-trip read of a chat row in one file, as `line  snippet`. */
function racyChatReads(source: string): Array<{ index: number, text: string }> {
  const found: Array<{ index: number, text: string }> = []
  // A read through a named locator: the name, any chain of calls off it
  // (`.first()`, `.nth(i)`, `.locator(…)`), then the read.
  for (const name of chatLocatorNames(source)) {
    const pattern = new RegExp(String.raw`\b${name}\b(?:\s*\.\w+\([^()]*\))*\s*${RACY_READ}`, 'g')
    for (const match of source.matchAll(pattern))
      found.push({ index: match.index, text: match[0] })
  }
  // A read straight off the helper that builds the locator, with no name in
  // between -- `bandRows(page).evaluateAll(…)`. Matched per line, because the
  // callback that follows spans many of them.
  let offset = 0
  for (const line of source.split('\n')) {
    const read = new RegExp(RACY_READ).exec(line)
    if (read && CHAT_LOCATOR_MARKERS.some(marker => line.includes(marker)))
      found.push({ index: offset + read.index, text: line.trim() })
    offset += line.length + 1
  }
  return found
}

describe('e2e chat row reads', () => {
  it('never reads a chat row through a two-round-trip evaluate', () => {
    const offenders = new Set<string>()
    for (const file of collectE2EFiles()) {
      const source = readFileSync(file, 'utf-8')
      for (const { index, text } of racyChatReads(source)) {
        const line = source.slice(0, index).split('\n').length
        offenders.add(`${posixRelative(frontendRoot, file)}:${line}  ${text}`)
      }
    }
    const hint = [
      'A chat row is re-created whenever its entry is replaced, so a handle resolved one round trip',
      'earlier can already be detached -- which reports an empty rect and NO computed style.',
      '`evaluateAll` pins its elements a round trip early too. Read through readAttached',
      '(tests/e2e/helpers/ui.ts), skipping any match whose `isConnected` is false, or measure the row',
      'with measureBubbleEdges / measureAgainstChatList:',
    ].join(' ')
    expect([...offenders].sort(), `${hint}\n  ${[...offenders].sort().join('\n  ')}`).toEqual([])
  })
})
