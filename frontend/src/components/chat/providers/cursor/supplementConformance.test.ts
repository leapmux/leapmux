import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { acpToolSupplement } from '../acp/toolSupplement'
import { cursorStoredToolOutput } from './extractors/storedTool'

/**
 * The browser's half of testdata/cursor_stored_tool_conformance.json.
 *
 * `TestCursorStoredToolConformance` in the worker replays the same file and asserts it
 * WRITES those bytes. Neither side resolves this payload into the frame, so the stored
 * bytes are the whole contract: a key that drifted leaves the worker writing a record
 * the browser then answers as absent, and a call the protocol left incomplete draws
 * with neither its arguments nor its output.
 */
const fixturePath = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../../../testdata/cursor_stored_tool_conformance.json')
const fixture = JSON.parse(readFileSync(fixturePath, 'utf8')) as {
  cases: Array<{ name: string, original: Record<string, unknown>, supplement: Record<string, unknown>, expected: Record<string, unknown> }>
}

describe('cursor stored-tool supplement conformance', () => {
  it('loads a nonempty shared corpus', () => {
    expect(fixture.cases.length).toBeGreaterThan(0)
  })

  it.each(fixture.cases)('$name', ({ original, supplement, expected }) => {
    // Through the SAME gate production applies: the browser reaches this record only
    // as `facts.extra`, which is the supplement that matched the row's own frame.
    const read = cursorStoredToolOutput(acpToolSupplement(original, supplement))
    // A `null` in the corpus states a field the reader must answer as absent.
    expect({
      content: read?.content,
      toolArguments: read?.toolArguments ?? null,
      providerOptions: read?.providerOptions ?? null,
    }).toEqual(expected)
  })

  it('answers a row that carries no stored record with null', () => {
    expect(cursorStoredToolOutput(undefined)).toBeNull()
    expect(cursorStoredToolOutput({ toolCallId: 'call-1' })).toBeNull()
    expect(cursorStoredToolOutput({ rawOutput: { content: 'not a list' } })).toBeNull()
  })

  // A record stored beside ANOTHER call reaches no row, because the gate refuses the
  // envelope before the parser ever sees it.
  it('reads nothing out of a record stored beside another call', () => {
    // The corpus is pinned nonempty above, so the guard is the type-level one alone.
    const first = fixture.cases[0]
    if (first === undefined)
      throw new Error('the conformance corpus states no cases')
    const { original, supplement } = first
    expect(acpToolSupplement({ ...original, toolCallId: 'other' }, supplement)).toBeUndefined()
    expect(cursorStoredToolOutput(acpToolSupplement({ ...original, toolCallId: 'other' }, supplement))).toBeNull()
  })
})
