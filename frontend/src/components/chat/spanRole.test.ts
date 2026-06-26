import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { spanRole } from './spanRole'

// spanRole only reads `parsed.parentObject`; build a minimal parsed shape whose
// `message.content` holds the Anthropic-style content blocks getMessageContent reads.
function parsedWithBlocks(blocks: unknown[]): ParsedMessageContent {
  return {
    rawText: '',
    topLevel: null,
    parentObject: { message: { content: blocks } },
    wrapper: null,
  }
}

function parsedWithType(type: string): ParsedMessageContent {
  return { rawText: '', topLevel: null, parentObject: { type }, wrapper: null }
}

describe('spanrole', () => {
  describe('claude/anthropic content blocks', () => {
    it('classifies a tool_use block as the opener', () => {
      expect(spanRole(AgentProvider.CLAUDE_CODE, parsedWithBlocks([{ type: 'tool_use' }]))).toBe('opener')
    })

    it('classifies a tool_result block as the result', () => {
      expect(spanRole(AgentProvider.CLAUDE_CODE, parsedWithBlocks([{ type: 'tool_result' }]))).toBe('result')
    })

    it('lets the tool_use opener win when a message carries BOTH block types', () => {
      // A message holding both a tool_use and a tool_result block IS the opener (it
      // carries the tool input to render); it must not be mis-bucketed as a result
      // just because a tool_result block appears -- regardless of block order.
      expect(spanRole(AgentProvider.CLAUDE_CODE, parsedWithBlocks([
        { type: 'tool_result' },
        { type: 'tool_use' },
      ]))).toBe('opener')
      expect(spanRole(AgentProvider.CLAUDE_CODE, parsedWithBlocks([
        { type: 'tool_use' },
        { type: 'tool_result' },
      ]))).toBe('opener')
    })

    it('skips non-object blocks and classifies text-only content as other', () => {
      expect(spanRole(AgentProvider.CLAUDE_CODE, parsedWithBlocks([null, 'str', { type: 'text' }]))).toBe('other')
    })

    it('returns other when there is no content array', () => {
      expect(spanRole(AgentProvider.CLAUDE_CODE, { rawText: '', topLevel: null, parentObject: undefined, wrapper: null })).toBe('other')
    })
  })

  describe('pi flat envelope', () => {
    it('routes tool_execution_start to opener and _end to result by type', () => {
      expect(spanRole(AgentProvider.PI, parsedWithType('tool_execution_start'))).toBe('opener')
      expect(spanRole(AgentProvider.PI, parsedWithType('tool_execution_end'))).toBe('result')
    })

    it('returns other for an unrelated pi envelope type', () => {
      expect(spanRole(AgentProvider.PI, parsedWithType('agent_message'))).toBe('other')
    })
  })
})
