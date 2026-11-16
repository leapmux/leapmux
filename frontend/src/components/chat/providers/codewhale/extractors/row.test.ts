import type { MessageCategory } from '../../../messageClassifier'
import type { ChatRow, ToolCallRow } from '../../../model/row'
import type { ResolvedMessageContent } from '~/components/chat/rowExtractionTypes'
import { describe, expect, it } from 'vitest'
import { CODEWHALE_BLOCK_TYPE, CODEWHALE_EVENT, CODEWHALE_ITEM_KIND, CODEWHALE_TOOL, CODEWHALE_TRANSCRIPT_ROLE } from '~/generated/contracts/codewhale-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { toolCallMeta } from '../../../results/tools/meta'
import { input } from '../../testUtils'
import { childBlock, codewhaleEvent, itemFinished, requestSide, toolCompleted, toolFailed, toolFinished, toolStarted } from '../toolResults.fixtures'
import { codewhaleExtractRow } from './row'

interface Span {
  request?: ResolvedMessageContent
}

function extract(payload: Record<string, unknown>, category: MessageCategory, span: Span = {}, completion?: MessageCompletion): ChatRow | null {
  return codewhaleExtractRow({
    resolved: input(payload, undefined, AgentProvider.CODEWHALE),
    category,
    span: {
      request: span.request,
      role: 'other',
      visibleRows: { request: true },
    },
    completion,
  } as never)
}

/** The final event of an answered question, which the runtime sends with its answers redacted. */
function redactedQuestionResult(): Record<string, unknown> {
  return codewhaleEvent(CODEWHALE_EVENT.ItemCompleted, {
    item: {
      kind: CODEWHALE_ITEM_KIND.ToolCall,
      status: 'completed',
      summary: 'User input submitted',
      detail: 'User input submitted',
      metadata: { tool_call_id: 'call-1', tool_name: CODEWHALE_TOOL.RequestUserInput, response_redacted: true },
    },
  })
}

function toolRow(row: ChatRow | null): ToolCallRow {
  expect(row?.kind).toBe('tool')
  return row as ToolCallRow
}

describe('codewhaleExtractRow', () => {
  it('reads a finished message and a finished reasoning step', () => {
    expect(extract(itemFinished(CODEWHALE_ITEM_KIND.AgentMessage, 'Hello.'), { kind: 'assistant_text' }))
      .toStrictEqual({ kind: 'assistant-text', text: 'Hello.' })
    expect(extract(itemFinished(CODEWHALE_ITEM_KIND.AgentReasoning, 'Thinking it over.'), { kind: 'assistant_thinking' }))
      .toStrictEqual({ kind: 'assistant-thinking', text: 'Thinking it over.' })
  })

  it('reads a subagent text block as the child\'s reply', () => {
    const block = childBlock(CODEWHALE_TRANSCRIPT_ROLE.Assistant, { type: CODEWHALE_BLOCK_TYPE.Text, text: 'PONG' })
    expect(extract(block, { kind: 'assistant_text' })).toStrictEqual({ kind: 'assistant-text', text: 'PONG' })
  })

  it('answers null for a message category that the row does not carry', () => {
    expect(extract(itemFinished(CODEWHALE_ITEM_KIND.Status, 'Saved.'), { kind: 'assistant_text' })).toBeNull()
  })

  it('reads a user row in the shared shape', () => {
    expect(extract({ content: 'Hi there' }, { kind: 'user_content' })).toMatchObject({ kind: 'user', text: 'Hi there' })
  })

  it('reads the notice LeapMux writes when the reader sends a plan into execution', () => {
    expect(extract({ content: 'Execute the plan.', planExecution: true }, { kind: 'plan_execution' })).toStrictEqual({ kind: 'plan-execution', text: 'Execute the plan.' })
  })

  it('reads a subagent thinking block as the child\'s reasoning', () => {
    const block = childBlock(CODEWHALE_TRANSCRIPT_ROLE.Assistant, { type: CODEWHALE_BLOCK_TYPE.Thinking, thinking: 'Count first.' })
    expect(extract(block, { kind: 'assistant_thinking' })).toStrictEqual({ kind: 'assistant-thinking', text: 'Count first.' })
  })

  it('answers null for a category it does not read', () => {
    expect(extract(itemFinished(CODEWHALE_ITEM_KIND.AgentMessage, 'Hello.'), { kind: 'unknown' })).toBeNull()
  })

  // A request row reads its own opening frame alone, so it states no answer and no
  // finished status. The result row reads its own final frame and the opening frame.
  it('reads a tool request and its result as two rows of one call, each in its own role', () => {
    const args = { command: 'ls' }
    const start = toolStarted(CODEWHALE_TOOL.Bash, args)
    const end = toolCompleted(CODEWHALE_TOOL.Bash, args, 'a.ts')
    const request = toolRow(extract(start, { kind: 'tool_use' }, {
      request: requestSide(start),
    }))
    expect(request.role).toBe('request')
    expect(request.call.status).toBe('in_progress')
    expect(request.call.result).toBeUndefined()
    expect(request.call.kind === 'execute' && request.call.request.command).toBe('ls')
    const result = toolRow(extract(end, { kind: 'tool_result' }, {
      request: requestSide(start),
    }))
    expect(result.role).toBe('result')
    expect(result.call.status).toBe('completed')
  })

  // The first call of a deferred tool loads the tool's schema and runs nothing. Only
  // its result row states the runtime's words, so that row states the call whole.
  it('states a deferred tool\'s first call on its result row, with the runtime\'s words', () => {
    const args = { patch: 'x' }
    const start = toolStarted(CODEWHALE_TOOL.ApplyPatch, args)
    const loaded = toolCompleted(CODEWHALE_TOOL.ApplyPatch, args, 'Tool `apply_patch` was deferred and has now been loaded.', { deferred_tool_loaded: true })
    const request = toolRow(extract(start, { kind: 'tool_use' }, {
      request: requestSide(start),
    }))
    expect(request.call.status).toBe('in_progress')
    expect(request.call.result).toBeUndefined()
    const row = toolRow(extract(loaded, { kind: 'tool_result' }, {
      request: requestSide(start),
    }))
    expect(row.role).toBe('result')
    expect(row.call.status).toBe('completed')
    expect(row.call.result).toStrictEqual({ unparsed: true, text: 'Tool `apply_patch` was deferred and has now been loaded.' })
  })

  // A schema load ran nothing. A file change that ended with no changes of its own states
  // the changes it asked for as the ones the file took, so the load must not be a file
  // change: its row states the runtime's words alone.
  it('states a deferred first call of a file change as a call that changed no file', () => {
    const args = { patch: '*** Begin Patch\n*** Add File: deferred.txt\n+hi\n*** End Patch\n' }
    const start = toolStarted(CODEWHALE_TOOL.ApplyPatch, args)
    const loaded = toolCompleted(CODEWHALE_TOOL.ApplyPatch, args, 'Tool `apply_patch` was deferred and has now been loaded.', { deferred_tool_loaded: true })
    expect(toolRow(extract(start, { kind: 'tool_use' }, {
      request: requestSide(start),
    })).call.kind).toBe('edit')
    const row = toolRow(extract(loaded, { kind: 'tool_result' }, {
      request: requestSide(start),
    }))
    expect(row.call.kind).toBe('other')
    expect(row.call.degradation).toBeUndefined()
    expect(row.call.result).toStrictEqual({ unparsed: true, text: 'Tool `apply_patch` was deferred and has now been loaded.' })
    expect(toolCallMeta(row).hasDiff).toBe(false)
  })

  // A load that failed applied nothing either, and a failed file change draws no
  // requested change, so it keeps its kind and its header.
  it('keeps the kind of a failed deferred first call of a file change, with no diff', () => {
    const args = { patch: '*** Begin Patch\n*** Add File: deferred.txt\n+hi\n*** End Patch\n' }
    const start = toolStarted(CODEWHALE_TOOL.ApplyPatch, args)
    const failed = toolFinished(CODEWHALE_EVENT.ItemFailed, CODEWHALE_TOOL.ApplyPatch, args, 'The schema did not load.', { deferred_tool_loaded: true })
    const row = toolRow(extract(failed, { kind: 'tool_result' }, {
      request: requestSide(start),
    }))
    expect(row.call.kind).toBe('edit')
    expect(row.call.status).toBe('failed')
    expect(row.call.result).toStrictEqual({ failure: true, text: 'The schema did not load.' })
    expect(toolCallMeta(row).hasDiff).toBe(false)
  })

  // The result row of a failed schema load draws, so it must state the failure as a
  // failure: an unparsed result on a failed call degrades the call to the
  // uncategorized row.
  it('states a failed deferred first call as a failure on its result row', () => {
    const args = { command: 'ls' }
    const start = toolStarted(CODEWHALE_TOOL.Bash, args)
    const failed = toolFinished(CODEWHALE_EVENT.ItemFailed, CODEWHALE_TOOL.Bash, args, 'The schema did not load.', { deferred_tool_loaded: true })
    const row = toolRow(extract(failed, { kind: 'tool_result' }, {
      request: requestSide(start),
    }))
    expect(row.call.kind).toBe('execute')
    expect(row.call.degradation).toBeUndefined()
    expect(row.call.status).toBe('failed')
    expect(row.call.result).toStrictEqual({ failure: true, text: 'The schema did not load.' })
  })

  // The runtime redacts a question's answers from the call's own result. The
  // classifier hides that result row, and the saved answer beside it states the reply.
  it('states an asked question on its request row', () => {
    const args = {
      questions: [
        {
          id: 'q1',
          header: 'Drink',
          question: 'Tea or coffee?',
          options: [
            { label: 'Tea', description: 'Tea' },
          ],
        },
      ],
    }
    const start = toolStarted(CODEWHALE_TOOL.RequestUserInput, args)
    const request = toolRow(extract(start, { kind: 'tool_use' }, {
      request: requestSide(start),
    }))
    expect(request.call.kind).toBe('question')
    expect(request.call.status).toBe('in_progress')
    expect(request.call.result).toBeUndefined()
    expect(request.call.kind === 'question' && request.call.request.questions.map(question => question.question)).toEqual(['Tea or coffee?'])
    const answered = toolRow(extract(redactedQuestionResult(), { kind: 'tool_result' }, {
      request: requestSide(start),
    }))
    expect(answered.call.kind).toBe('question')
    expect(answered.call.status).toBe('completed')
    expect(answered.call.result).toStrictEqual({ answers: [] })
  })

  it('keeps the result row of a question that failed', () => {
    const args = { questions: [] }
    const start = toolStarted(CODEWHALE_TOOL.RequestUserInput, args)
    const failed = toolFailed(CODEWHALE_TOOL.RequestUserInput, args, 'questions must not be empty')
    expect(toolRow(extract(failed, { kind: 'tool_result' }, {
      request: requestSide(start),
    })).call.status).toBe('failed')
  })

  // A subagent's result block states neither the tool nor its arguments, so its row
  // reads them from the `tool_use` block before it.
  it('states a subagent\'s deferred first call on its result row', () => {
    const words = 'Tool `apply_patch` was deferred and has now been loaded. Retry the call with the newly available schema.'
    const use = childBlock(CODEWHALE_TRANSCRIPT_ROLE.Assistant, {
      type: CODEWHALE_BLOCK_TYPE.ToolUse,
      id: 'b1',
      name: CODEWHALE_TOOL.ApplyPatch,
      input: { patch: 'x' },
    })
    const loaded = childBlock(CODEWHALE_TRANSCRIPT_ROLE.User, { type: CODEWHALE_BLOCK_TYPE.ToolResult, tool_use_id: 'b1', content: words }, 2)
    expect(toolRow(extract(use, { kind: 'tool_use' }, {
      request: requestSide(use),
    })).call.result).toBeUndefined()
    const row = toolRow(extract(loaded, { kind: 'tool_result' }, {
      request: requestSide(use),
    }))
    expect(row.call.name).toBe(CODEWHALE_TOOL.ApplyPatch)
    expect(row.call.result).toStrictEqual({ unparsed: true, text: words })
  })

  // LeapMux's own reading of how the row ended wins: a turn the reader stopped
  // leaves the start frame stored, and the row must not draw a finished call.
  it('words a retained call that the reader interrupted as cancelled', () => {
    const row = toolRow(extract(toolStarted(CODEWHALE_TOOL.Bash, { command: 'sleep 30' }), { kind: 'tool_use' }, {}, MessageCompletion.INTERRUPTED))
    expect(row.call.status).toBe('cancelled')
  })

  it('answers null for a tool category whose row carries no call', () => {
    expect(extract(itemFinished(CODEWHALE_ITEM_KIND.AgentMessage, 'Hello.'), { kind: 'tool_use' })).toBeNull()
  })
})
