import type { ToolFailureFixture, ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { QWEN_TOOL } from '~/generated/contracts/qwen-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { describeToolFailureLadder, describeToolResultCorpus } from '~/test-support/toolResultCases'
import { fixturesOnTheUncategorizedKind, openingFrameOf } from '~/test-support/toolVocabulary'
import { QWEN_TOOL_KINDS } from './toolKinds'
import { QWEN_TOOL_RESULTS } from './toolResults.fixtures'
import '~/components/chat/providers'

const KINDS: ToolVocabularyCheck = {
  names: [...Object.keys(QWEN_TOOL_KINDS), QWEN_TOOL.Agent, QWEN_TOOL.Workflow, QWEN_TOOL.TodoWrite],
  kindOf: () => 'other',
  generic: {},
  fallback: 'other',
}

// Each name comes from the fixture table's own keys, so the lookup misses for the type alone.
function callOf(name: string) {
  const fixture = QWEN_TOOL_RESULTS.fixtures[name]
  return fixture === undefined ? null : providerToolCall(AgentProvider.QWEN_CODE, fixture.payload, fixture.options)
}
const failureCallOf = (fixture: ToolFailureFixture) => providerToolCall(AgentProvider.QWEN_CODE, fixture.payload, fixture.options)

/** The `tool_call` request that each fixture pairs with, read alone as a call still in flight. */
function requestCallOf(name: string) {
  const fixture = QWEN_TOOL_RESULTS.fixtures[name]
  const frame = fixture === undefined ? null : openingFrameOf(fixture)
  return frame === null ? null : providerToolCall(AgentProvider.QWEN_CODE, frame)
}

describe('qwen tool results', () => {
  it('keeps every fixture off the uncategorized kind', () => {
    expect(fixturesOnTheUncategorizedKind(QWEN_TOOL_RESULTS, callOf), 'A fixture reached the uncategorized kind.').toStrictEqual([])
  })

  it('answers each fixture with the kind its name states', () => {
    const expected: Record<string, string> = {
      ...QWEN_TOOL_KINDS,
      [QWEN_TOOL.Agent]: 'agent',
      [QWEN_TOOL.Workflow]: 'agent',
      [QWEN_TOOL.TodoWrite]: 'todo',
    }
    for (const [name, kind] of Object.entries(expected)) {
      const call = callOf(name)
      expect(call, name).not.toBeNull()
      expect(call!.kind, name).toBe(kind)
      expect(call!.name, name).toBe(name)
    }
  })

  describeToolResultCorpus(KINDS, QWEN_TOOL_RESULTS, callOf)
  describeToolFailureLadder(QWEN_TOOL_RESULTS, { callOf, failureCallOf, requestCallOf })
})
