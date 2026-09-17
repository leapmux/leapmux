import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { lineNumberAt, stripCommentLines } from '~/test-support/sourceScan'
import { collectFiles, frontendRoot, posixRelative } from '~/test-support/sourceTree'

// The tool-call IR pairs a `kind` with the request and the result that kind declares,
// and the renderers read those fields WITHOUT a guard -- `moveRenderer` reads
// `request.changes[0]`, `listRenderer` reads `request.path`. A call whose kind and
// request disagree therefore does not draw wrong, it THROWS, and the error boundary
// replaces the whole message.
//
// An assertion is the only way to write that disagreement. Twenty-eight of them once
// sat on this path, and one was load-bearing for a real defect: `acpPayloadFor`
// declared it answered an `mcp` payload, listed `mcp` in no case of its switch, and
// returned `kind: ''` from its default case. Cursor's branch then tested
// `shared.kind === 'mcp'`, never matched, and drew the card with no body at all.
//
// They are gone, and the compiler now checks every producer. This guard is what keeps
// the count at zero: the next provider that meets a type error has to fix the shape
// rather than silence the checker.

const CHAT_DIR = join(frontendRoot, 'src/components/chat')

/**
 * The assertions this guard refuses, by the type they assert TO.
 *
 * Each one re-pairs a kind with a payload, a request or a result. `as const`, `as unknown`
 * on its own, and an assertion to any other type are not this rule's business.
 *
 * `ToolCallIR` and `ToolResultOf<` are the two the list opened without, and each is
 * a union over EVERY kind -- which makes them the two a widening assertion reaches for
 * first. An assertion needs only COMPARABILITY with one member, and a member is
 * comparable to a literal that states a subset of its fields. So `frame as ToolCallIR`
 * compiles for an UNDER-FILLED literal: `{ kind: 'edit', id: 'c1' }` passes, and the
 * renderer that reads `request.changes[0]` on it throws.
 *
 * `ParsedCall<` and `ResolvedCall<` joined later, for the same reason from the other
 * side: they pair a renderer's HOOK with a call, over the contravariance the hook
 * parameters declare. `dispatchToolCall` in `results/tools/index.ts` owns that pairing
 * once, over the total table, so an assertion to either type anywhere is a second
 * pairing nobody checked.
 */
const FORBIDDEN = [
  'ToolCallIR',
  'ToolCallPayloadIR',
  'ToolCallPayloadOf',
  'ToolCallPayload<',
  'ToolCallOf<',
  'ToolCallOfKinds<',
  'ToolRequests[',
  'ToolResults[',
  'ToolResultOf<',
  'ParsedCall<',
  'ResolvedCall<',
]

/**
 * The IR's own builder, which is where the kind meets the invariants.
 *
 * `buildToolCall` checks the lifecycle rules at RUNTIME, over a draft whose status
 * came from the wire. Those rules ARE the lifecycle union's own members, one for one,
 * but no narrowing carries a runtime answer back into the type system: TypeScript
 * cannot see that a draft which passed `toolCallFault` is a `CompletedCall` rather
 * than a `FailedCall`. The assertion stands on the check immediately above it, and it
 * is the reason every OTHER site can stay unasserted -- a provider hands the builder
 * a payload and gets a checked call back.
 *
 * ONE file, exact rather than a prefix, for the same reason the renderer list is.
 */
const IR_BUILDER_ALLOWED = new Set(['ir/toolCall.ts'])

/**
 * The renderer layer's own dispatch, which is where the renderer meets the call.
 *
 * `ToolKindRenderer` declares its hooks as property functions, and several are
 * OPTIONAL -- so their parameters are contravariant in the kind, and
 * `ToolKindRenderer<'read'>` is not assignable to `ToolKindRenderer<ToolKind>`. A
 * caller therefore cannot name the type that pairs one call with one renderer, and
 * the pair was once re-stated by assertion at every hook a component invoked.
 *
 * `dispatchToolCall` answers that pair ONCE: it selects over the table that is total
 * and keyed by the same literal `kind` the call holds, and hands the operation a
 * correlated renderer, call, parsed view and resolved view. No file needs an
 * exemption for the pairing, which is why no allow-list follows this note.
 */

/** The implementation modules a guard reads: never a test, a fixture or a harness. */
function chatModules(): string[] {
  return collectFiles(CHAT_DIR, {
    matches: name => (name.endsWith('.ts') || name.endsWith('.tsx'))
      && !name.endsWith('.test.ts')
      && !name.endsWith('.test.tsx')
      && !name.endsWith('.css.ts')
      // Test DATA co-located with the module it describes; see `providerLayering.test.ts`.
      && !name.endsWith('.fixtures.ts')
      && name !== 'testUtils.ts'
      && name !== 'testUtils.tsx',
  })
}

/** Each `as <Type>` in one file, with the line it sits on. Comments are stripped first. */
function assertionsTo(source: string, types: string[]): Array<{ type: string, line: number }> {
  const text = stripCommentLines(source)
  const found: Array<{ type: string, line: number }> = []
  for (const type of types) {
    const needle = `as ${type}`
    for (let at = text.indexOf(needle); at >= 0; at = text.indexOf(needle, at + 1))
      found.push({ type, line: lineNumberAt(text, at) })
  }
  return found
}

describe('the tool-call IR carries no assertion', () => {
  it('finds the modules it guards', () => {
    expect(chatModules().length).toBeGreaterThan(100)
  })

  // The needle list IS the guard, and a needle that matches nothing looks exactly like
  // a tree that holds nothing: a green suite either way. This pins each entry to the
  // text it must find, and pins the comment stripping that keeps this file's own
  // explanations out of the count.
  it('reads each assertion its needle list states, and none out of a comment', () => {
    const found = assertionsTo(FORBIDDEN.map(type => `const call = frame as ${type}`).join('\n'), FORBIDDEN)
    expect(found.map(entry => entry.type).sort()).toEqual([...FORBIDDEN].sort())
    expect(assertionsTo('// `frame as ToolCallIR` is the form this guard refuses.\n', FORBIDDEN)).toEqual([])
  })

  // `as const` and a bare `as unknown` re-pair nothing, and neither does an assertion
  // to a type outside the tool-call family. A rule that reported them would teach the
  // next reader to widen the allow-list rather than to correct a real pairing.
  it('reads no assertion that re-pairs nothing', () => {
    const neutral = 'const kinds = [\'read\'] as const\nconst raw = frame as unknown\nconst kind = word as ToolKind\n'
    expect(assertionsTo(neutral, FORBIDDEN)).toEqual([])
  })

  it('pairs no kind with a payload by assertion', () => {
    const offences: string[] = []
    for (const file of chatModules()) {
      const relative = posixRelative(CHAT_DIR, file)
      if (IR_BUILDER_ALLOWED.has(relative))
        continue
      for (const { type, line } of assertionsTo(readFileSync(file, 'utf8'), FORBIDDEN))
        offences.push(`${relative}:${line} asserts to ${type}`)
    }
    expect(
      offences,
      'An assertion here re-pairs a kind with a request or a result the kind does not declare, '
      + 'and the renderers read those fields with no guard -- so the row throws rather than draws. '
      + 'Build the payload at a LITERAL kind instead: narrow with `if (kind === ...)` and return '
      + 'inside the branch, or, for a function generic in the kind, answer from a mapped table '
      + '(`ACP_PAYLOAD_BUILDERS` is the pattern). Hand the finished payload to `toolCall`, which '
      + 'is the one function allowed to state the pairing.',
    ).toEqual([])
  })

  // The builder's exemption is ONE assertion, not a standing permission for the file.
  // A second one there is a new pairing nobody checked, and it would hide behind the
  // first.
  it('keeps the IR builder to the single assertion its check earns', () => {
    for (const relative of IR_BUILDER_ALLOWED) {
      const found = assertionsTo(readFileSync(join(CHAT_DIR, relative), 'utf8'), FORBIDDEN)
      expect(found.map(entry => `${relative}:${entry.line} asserts to ${entry.type}`)).toHaveLength(1)
    }
  })
})
