import type { ToolFailureFixture, ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { GROK_TOOL } from '~/generated/contracts/grok-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { describeToolFailureLadder, describeToolResultCorpus } from '~/test-support/toolResultCases'
import { fixturesOnTheUncategorizedKind, openingFrameOf } from '~/test-support/toolVocabulary'
import { GROK_TOOL_KINDS, GROK_TOOL_NAME } from './toolKinds'
import { GROK_TOOL_RESULTS } from './toolResults.fixtures'
import '~/components/chat/providers'

const KINDS: ToolVocabularyCheck = {
  names: [...Object.keys(GROK_TOOL_KINDS), GROK_TOOL.SpawnSubagent, GROK_TOOL_NAME.TodoWrite, GROK_TOOL_NAME.UseTool, GROK_TOOL_NAME.Workflow],
  kindOf: () => 'other',
  generic: {},
  fallback: 'other',
}

// Each name comes from the fixture table's own keys, so the lookup misses for the type alone.
function callOf(name: string) {
  const fixture = GROK_TOOL_RESULTS.fixtures[name]
  return fixture === undefined ? null : providerToolCall(AgentProvider.GROK_BUILD, fixture.payload, fixture.options)
}
const failureCallOf = (fixture: ToolFailureFixture) => providerToolCall(AgentProvider.GROK_BUILD, fixture.payload, fixture.options)

/** The `tool_call` request that each fixture pairs with, read alone as a call still in flight. */
function requestCallOf(name: string) {
  const fixture = GROK_TOOL_RESULTS.fixtures[name]
  const frame = fixture === undefined ? null : openingFrameOf(fixture)
  return frame === null ? null : providerToolCall(AgentProvider.GROK_BUILD, frame)
}

describe('grok tool results', () => {
  it('keeps every fixture off the uncategorized kind', () => {
    expect(fixturesOnTheUncategorizedKind(GROK_TOOL_RESULTS, callOf), 'A fixture reached the uncategorized kind.').toStrictEqual([])
  })

  it('answers each fixture with the kind its name states', () => {
    const expected: Record<string, string> = {
      ...GROK_TOOL_KINDS,
      [GROK_TOOL.SpawnSubagent]: 'agent',
      [GROK_TOOL_NAME.Workflow]: 'agent',
      [GROK_TOOL_NAME.TodoWrite]: 'todo',
      [GROK_TOOL_NAME.UseTool]: 'mcp',
    }
    for (const [name, kind] of Object.entries(expected)) {
      const call = callOf(name)
      expect(call, name).not.toBeNull()
      expect(call!.kind, name).toBe(kind)
      expect(call!.name, name).toBe(name)
    }
  })

  describeToolResultCorpus(KINDS, GROK_TOOL_RESULTS, callOf)
  describeToolFailureLadder(GROK_TOOL_RESULTS, { callOf, failureCallOf, requestCallOf })
})
