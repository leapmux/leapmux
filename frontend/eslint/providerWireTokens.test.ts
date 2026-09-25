import type { ProviderFrameKind } from '../src/generated/contracts/provider-frame-kinds'
import { describe, expect, it } from 'vitest'
import { isDistinctiveWord, PROVIDER_WIRE_TOKENS, wireTokenIndex, wireTokenSources } from './providerWireTokens'

function name(literal: string, source = 'test-protocol events'): ProviderFrameKind {
  return { literal, match: 'name', source }
}

function prefix(literal: string, source = 'test-protocol eventPrefixes'): ProviderFrameKind {
  return { literal, match: 'prefix', source }
}

describe('isDistinctiveWord', () => {
  it.each(['tool_call', 'item.started', 'cursor/ask_question', 'commandExecution', 'ApiRetry'])('accepts %s', (word) => {
    expect(isDistinctiveWord(word)).toBe(true)
  })

  // An ordinary word, a capitalized word and a hyphenated word can each be shared
  // code's own spelling: a status, a type name, a class name or a test id.
  it.each(['', 'error', 'plan', 'Task', 'agent-subtask', 'tool-call'])('rejects %j', (word) => {
    expect(isDistinctiveWord(word)).toBe(false)
  })
})

describe('wireTokenIndex', () => {
  it('holds nothing for no frame kinds', () => {
    const index = wireTokenIndex([])
    expect(index.names.size).toBe(0)
    expect(index.prefixes).toEqual([])
    expect(wireTokenSources(index, 'item.started')).toEqual([])
  })

  it('guards a distinctive name, and leaves an ordinary word to shared code', () => {
    const index = wireTokenIndex([name('item.started'), name('error')])
    expect(wireTokenSources(index, 'item.started')).toEqual(['test-protocol events'])
    expect(wireTokenSources(index, 'error')).toEqual([])
  })

  // A name matches the whole literal only. A word that only contains a token is
  // not the token.
  it('matches a name at both ends', () => {
    const index = wireTokenIndex([name('item.started'), name('tool.result')])
    expect(wireTokenSources(index, 'item.startedAt')).toEqual([])
    expect(wireTokenSources(index, 'last.tool.result')).toEqual([])
  })

  it('guards each literal that starts with a distinctive prefix, and the prefix itself', () => {
    const index = wireTokenIndex([prefix('session.canvas.')])
    expect(wireTokenSources(index, 'session.canvas.opened')).toEqual(['test-protocol eventPrefixes'])
    expect(wireTokenSources(index, 'session.canvas.')).toEqual(['test-protocol eventPrefixes'])
    expect(wireTokenSources(index, 'session.canvas')).toEqual([])
  })

  // `model.` starts Copilot's model events, and shared code spells `model.row` as a
  // cache key. A prefix whose stem is an ordinary word would claim that key.
  it('leaves open a prefix whose stem is an ordinary word', () => {
    const index = wireTokenIndex([prefix('model.'), prefix('assistant.fusion_')])
    expect(index.prefixes.map(entry => entry.prefix)).toEqual(['assistant.fusion_'])
    expect(wireTokenSources(index, 'model.row')).toEqual([])
    expect(wireTokenSources(index, 'assistant.fusion_step')).toEqual(['test-protocol eventPrefixes'])
  })

  it('guards the namespace of a method when the namespace is distinctive', () => {
    const index = wireTokenIndex([name('_kiro/userInput', 'kiro-protocol methods'), name('_kiro/mcp/elicitation', 'kiro-protocol methods')])
    expect(index.prefixes).toEqual([{ prefix: '_kiro/', sources: ['kiro-protocol methods'] }])
    expect(wireTokenSources(index, '_kiro/steer')).toEqual(['kiro-protocol methods'])
    expect(wireTokenSources(index, '_kiro/')).toEqual(['kiro-protocol methods'])
  })

  it('guards a method whose namespace is an ordinary word by its whole name only', () => {
    const index = wireTokenIndex([name('cursor/ask_question', 'cursor-protocol methods')])
    expect(index.prefixes).toEqual([])
    expect(wireTokenSources(index, 'cursor/ask_question')).toEqual(['cursor-protocol methods'])
    expect(wireTokenSources(index, 'cursor/pointer')).toEqual([])
  })

  it('states every table that owns a literal, once each', () => {
    const index = wireTokenIndex([
      name('turn.completed', 'zcode-protocol events'),
      name('turn.completed', 'codewhale-protocol events'),
      name('turn.completed', 'zcode-protocol events'),
    ])
    expect(wireTokenSources(index, 'turn.completed')).toEqual(['zcode-protocol events', 'codewhale-protocol events'])
  })

  it('states the owners of a name and of a prefix that both match', () => {
    const index = wireTokenIndex([name('_x.ai/exit_plan_mode', 'grok-protocol methods'), prefix('_x.ai/', 'test-protocol prefixes')])
    expect(wireTokenSources(index, '_x.ai/exit_plan_mode')).toEqual(['grok-protocol methods', 'test-protocol prefixes'])
  })
})

// The list comes from the contracts. These cases pin a token of each kind of table,
// so a table that loses its `frameKind` mark fails here with its own name.
describe('PROVIDER_WIRE_TOKENS', () => {
  it.each([
    ['session.idle', 'copilot-protocol events'],
    ['session.task_complete', 'copilot-protocol events'],
    ['session.event', 'copilot-protocol methods'],
    ['assistant.fusion_step', 'copilot-protocol eventPrefixes'],
    ['question.asked', 'opencode-protocol events'],
    ['session.created', 'zcode-protocol events'],
    ['interaction/requestUserInput', 'zcode-protocol methods'],
    ['plan_approval', 'zcode-protocol interactions'],
    ['compact_boundary', 'claude-protocol systemSubtypes'],
    ['microcompact_boundary', 'claude-protocol systemSubtypes'],
    ['agent_message_chunk', 'acp-protocol updates'],
    ['commandExecution', 'codex-protocol itemTypes'],
    ['turn/plan/updated', 'codex-protocol methods'],
    ['_x.ai/session_notification', 'grok-protocol methods'],
    ['turn_completed', 'grok-protocol notifications'],
    ['_kiro/userInput', 'kiro-protocol methods'],
    ['turn_end', 'kiro-protocol metaKinds'],
    ['_qwencode/end_turn', 'qwen-protocol methods'],
    ['cursor/create_plan', 'cursor-protocol methods'],
    ['event.approval.requested', 'kimi-protocol events'],
    ['message.part.updated', 'mimo-protocol events'],
    ['subagent_transcript_header', 'codewhale-protocol transcriptKinds'],
    ['user_input', 'codewhale-protocol replyFrames'],
    ['tool_executor.askQuestion', 'cline-protocol capabilities'],
    ['run_completed', 'cline-protocol teamRunEvents'],
    ['leapmux_amp_permission', 'amp-protocol permissionRequestTypes'],
    ['error_during_execution', 'amp-protocol resultSubtypes'],
    ['leapmux_ask_answer', 'ohmypi-protocol askTypes'],
    ['setStatus', 'pi-protocol extensionMethods'],
    ['agent_end', 'pi-protocol events'],
    ['text_delta', 'pi-protocol assistantEvents'],
  ])('rejects %s from %s', (token, source) => {
    expect(wireTokenSources(PROVIDER_WIRE_TOKENS, token)).toContain(source)
  })

  it.each(['error', 'plan', 'model.row', 'cursor/pointer', 'item.startedAt', 'status'])('leaves %s to shared code', (literal) => {
    expect(wireTokenSources(PROVIDER_WIRE_TOKENS, literal)).toEqual([])
  })
})
