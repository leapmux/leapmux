import type { ToolFailureFixture, ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallIr'
import { describeToolFailureLadder, describeToolResultCorpus } from '~/test-support/toolResultCases'
import { fixturesOnTheUncategorizedKind, openingFrameOf } from '~/test-support/toolVocabulary'
import { REASONIX_TOOL_RESULTS } from './toolResults.fixtures'
import '~/components/chat/providers'

const KINDS: ToolVocabularyCheck = {
  names: Object.keys(REASONIX_TOOL_RESULTS.fixtures),
  kindOf: () => 'other',
  generic: {},
  fallback: 'other',
}

// Each name comes from the fixture table's own keys, so the lookup misses for the type alone.
function callOf(name: string) {
  const fixture = REASONIX_TOOL_RESULTS.fixtures[name]
  return fixture === undefined ? null : providerToolCall(AgentProvider.REASONIX, fixture.payload, fixture.options)
}
const failureCallOf = (fixture: ToolFailureFixture) => providerToolCall(AgentProvider.REASONIX, fixture.payload, fixture.options)

/** The `tool_call` opener each fixture pairs with, read alone as a call still in flight. */
function openerCallOf(name: string) {
  const fixture = REASONIX_TOOL_RESULTS.fixtures[name]
  const frame = fixture === undefined ? null : openingFrameOf(fixture)
  return frame === null ? null : providerToolCall(AgentProvider.REASONIX, frame)
}

describe('reasonix tool results', () => {
  it('keeps every fixture off the uncategorized kind', () => {
    // The narrow form of the kind question, and the only form this table can hold: it
    // answers `other` for every name. Reasonix identifies every tool by its title, so
    // a fixture on the uncategorized card lost the name its title carried.
    expect(
      fixturesOnTheUncategorizedKind(REASONIX_TOOL_RESULTS, callOf),
      'A fixture reached the uncategorized kind.',
    ).toStrictEqual([])
  })

  it('answers each fixture with the kind its title states', () => {
    const expected: Record<string, string> = {
      read_file: 'read',
      view_image: 'read',
      glob: 'glob',
      grep: 'grep',
      ls: 'list',
      edit_file: 'edit',
      multi_edit: 'edit',
      write_file: 'write',
      move_file: 'move',
      delete_range: 'delete',
      bash: 'execute',
      web_fetch: 'fetch',
      task: 'agent',
      todo_write: 'todo',
      lookup: 'mcp',
    }
    for (const [name, kind] of Object.entries(expected)) {
      const call = callOf(name)
      expect(call, name).not.toBeNull()
      expect(call!.kind, name).toBe(kind)
    }
  })

  describeToolResultCorpus(KINDS, REASONIX_TOOL_RESULTS, callOf)
  describeToolFailureLadder(REASONIX_TOOL_RESULTS, { callOf, failureCallOf, openerCallOf })
})
