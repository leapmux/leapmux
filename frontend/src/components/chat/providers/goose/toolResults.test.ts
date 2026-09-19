import type { ToolFailureFixture, ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallIr'
import { describeToolFailureLadder, describeToolResultCorpus } from '~/test-support/toolResultCases'
import { fixturesOnTheUncategorizedKind, openingFrameOf } from '~/test-support/toolVocabulary'
import { GOOSE_TOOL_RESULTS } from './toolResults.fixtures'
import '~/components/chat/providers'

const KINDS: ToolVocabularyCheck = {
  names: Object.keys(GOOSE_TOOL_RESULTS.fixtures),
  kindOf: () => 'other',
  generic: {},
  fallback: 'other',
}

// Each name comes from the fixture table's own keys, so the lookup misses for the type alone.
function callOf(name: string) {
  const fixture = GOOSE_TOOL_RESULTS.fixtures[name]
  return fixture === undefined ? null : providerToolCall(AgentProvider.GOOSE, fixture.payload, fixture.options)
}
const failureCallOf = (fixture: ToolFailureFixture) => providerToolCall(AgentProvider.GOOSE, fixture.payload, fixture.options)

/** The `tool_call` request that each fixture pairs with, read alone as a call still in flight. */
function requestCallOf(name: string) {
  const fixture = GOOSE_TOOL_RESULTS.fixtures[name]
  const frame = fixture === undefined ? null : openingFrameOf(fixture)
  return frame === null ? null : providerToolCall(AgentProvider.GOOSE, frame)
}

describe('goose tool results', () => {
  it('keeps every fixture off the uncategorized kind', () => {
    // The narrow form of the kind question, and the only form this table can hold: it
    // answers `other` for every name. Goose identifies every tool through the `_meta`
    // record, so a fixture on the uncategorized card lost the name the metadata
    // carried.
    expect(
      fixturesOnTheUncategorizedKind(GOOSE_TOOL_RESULTS, callOf),
      'A fixture reached the uncategorized kind.',
    ).toStrictEqual([])
  })

  it('answers each fixture with the kind its metadata states', () => {
    const expected: Record<string, string> = {
      shell: 'execute',
      edit: 'edit',
      write: 'write',
      read: 'read',
      read_image: 'read',
      tree: 'list',
      delegate: 'agent',
      todo_write: 'todo',
      recall: 'mcp',
    }
    for (const [name, kind] of Object.entries(expected)) {
      const call = callOf(name)
      expect(call, name).not.toBeNull()
      expect(call!.kind, name).toBe(kind)
    }
  })

  describeToolResultCorpus(KINDS, GOOSE_TOOL_RESULTS, callOf)
  describeToolFailureLadder(GOOSE_TOOL_RESULTS, { callOf, failureCallOf, requestCallOf })
})
