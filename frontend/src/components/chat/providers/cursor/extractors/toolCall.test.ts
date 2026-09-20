import type { ToolCall } from '../../../model/toolCall'
import { describe, expect, it } from 'vitest'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { isToolFailureResult, isUnparsedToolResult, typedResult } from '../../../model/toolCall'
import { acpToolCall } from '../../acp/extractors/toolCall'
import { cursorSearchKind, cursorToolCallAdapter } from './toolCall'

const CALL = 'cursor-tool'

function call(tool: Record<string, unknown>, supplemental?: Record<string, unknown>): ToolCall {
  const frame = { sessionUpdate: 'tool_call_update', toolCallId: CALL, status: 'completed', ...tool }
  const extra = supplemental
    ? { sessionUpdate: frame.sessionUpdate, toolCallId: frame.toolCallId, status: frame.status, ...supplemental }
    : undefined
  return acpToolCall(frame, cursorToolCallAdapter, extra)
}

describe('cursor search kind', () => {
  // The rendered title and the result counters both describe the search, and they
  // disagree for a file search whose result also states a match total. The title is
  // the stronger statement, so the counters no longer overrule it.
  it.each([
    ['Find', { totalMatches: 4 }],
    ['Find `*.ts`', { totalMatches: 4 }],
    ['Find', { resultCount: 4 }],
  ])('keeps a file search a file search when the title states one (%s)', (title, raw) => {
    const frame = { sessionUpdate: 'tool_call_update', toolCallId: CALL, status: 'completed', kind: 'search', title, rawInput: { pattern: '*.ts' }, rawOutput: raw }
    expect(cursorSearchKind(frame, raw)).toBe('glob')
    expect(call({ kind: 'search', title, rawInput: { pattern: '*.ts' }, rawOutput: raw }).kind).toBe('glob')
  })

  it.each([
    ['grep', { totalFiles: 3 }],
    ['grep "needle"', { totalFiles: 3, totalMatches: 9 }],
  ])('keeps a content search a content search when the title states one (%s)', (title, raw) => {
    expect(call({ kind: 'search', title, rawInput: { pattern: 'needle' }, rawOutput: raw }).kind).toBe('grep')
  })

  // Cursor builds the grep title from the arguments: `grep`, then one flag for each
  // argument, then the quoted pattern last. A flag moves the pattern away from the front,
  // so a match on `grep "` alone misses every grep call that carries one. Each case here
  // also states the file-search counter, so only the title can produce a content search.
  it.each([
    'grep -i "needle"',
    'grep -n -A 3 "needle"',
    'grep -l',
    'grep --include="*.ts" "needle"',
    'grep | head -20 "needle"',
  ])('reads a content search from a grep title that carries a flag (%s)', (title) => {
    expect(call({ kind: 'search', title, rawInput: { pattern: 'needle' }, rawOutput: { totalFiles: 3 } }).kind).toBe('grep')
  })

  // A call that has not finished carries no counter, and a failed one reports its error
  // alone. The title is the only statement left in both states.
  it('reads a content search from a flagged grep title before the call finishes', () => {
    expect(call({ sessionUpdate: 'tool_call', status: 'pending', kind: 'search', title: 'grep -l "needle"', rawInput: { pattern: 'needle' } }).kind).toBe('grep')
  })

  // The reason is the RESULT of a call that failed, which is what a `ToolFailureResult`
  // states: the call ended without its payload and said only these words. The shared
  // ladder answers it for every kind, so the repaired kind answers it too.
  it('reads a content search from a flagged grep title when the call failed', () => {
    const failed = call({ kind: 'search', status: 'failed', title: 'grep -i "needle"', rawInput: { pattern: 'needle' }, rawOutput: { error: 'no such path' } })
    expect(failed.kind).toBe('grep')
    expect(isToolFailureResult(failed.result) && failed.result.text).toBe('no such path')
  })

  // A file-name search states its path, its pattern, both, or neither.
  it.each([
    'Find',
    'Find `src`',
    'Find `*.ts`',
    'Find `src` `*.ts`',
  ])('reads a file search from every title the runtime composes (%s)', (title) => {
    expect(call({ kind: 'search', title, rawInput: { pattern: '*.ts' }, rawOutput: { totalMatches: 9 } }).kind).toBe('glob')
  })

  it('falls back to the counters when the title states neither shape', () => {
    expect(call({ kind: 'search', title: 'Search', rawInput: { pattern: '*.ts' }, rawOutput: { totalFiles: 3 } }).kind).toBe('glob')
    expect(call({ kind: 'search', title: 'Search', rawInput: { pattern: 'needle' }, rawOutput: { totalMatches: 9 } }).kind).toBe('grep')
  })

  // The frame states the words the search printed, because a completed call answers
  // (invariant I2). The counters are what this case withholds, and they are the only
  // thing the shape reading takes from the result.
  it('leaves the kind alone when neither the title nor a counter states a shape', () => {
    const searched = call({
      kind: 'search',
      title: 'Search',
      rawInput: { pattern: 'needle' },
      rawOutput: {},
      content: [{ type: 'content', content: { type: 'text', text: 'one hit' } }],
    })
    expect(searched.kind).toBe('search')
  })

  it('reads no counter from a call that has not finished', () => {
    expect(call({ sessionUpdate: 'tool_call', status: 'pending', kind: 'search', title: 'Search', rawInput: { pattern: 'needle' }, rawOutput: { totalFiles: 3 } }).kind).toBe('search')
  })
})

describe('cursor protocol errors', () => {
  /** One failed MCP call, with whatever its saved tool result still holds. */
  function failedServerCall(saved: string) {
    return call({
      status: 'failed',
      kind: 'other',
      title: 'mcp_server_lookup',
      rawInput: { toolName: 'lookup', providerIdentifier: 'server' },
      rawOutput: { error: 'server unavailable' },
    }, {
      rawOutput: { content: [{ type: 'tool-result', toolCallId: CALL, toolName: 'mcp_server_lookup', result: saved }] },
    })
  }

  // A call that FAILED states a failure, not an unparsed payload: the two draw the
  // same, and the words mean different things. `UnparsedToolResult` says this build could
  // not read the answer into the kind's shape, which is not what happened.
  it('states the protocol error when a saved result restores no output', () => {
    const errored = failedServerCall('')
    expect(errored.kind).toBe('mcp')
    expect(isUnparsedToolResult(errored.result)).toBe(false)
    expect(isToolFailureResult(errored.result) && errored.result.text).toBe('server unavailable')
  })

  // The saved content of a failed call is not the card's body. Restoring it as one
  // drew a normal successful card, and the reason the server gave reached nobody. Both
  // halves state something the other does not, so both survive.
  it('states the protocol error above the partial output the record still holds', () => {
    const restored = failedServerCall('partial answer')
    expect(restored.kind).toBe('mcp')
    expect(isToolFailureResult(restored.result) && restored.result.text).toBe('server unavailable\n\npartial answer')
  })

  // A call the reader STOPPED is not a failure. It saved the part of the answer that
  // arrived, and that is what they asked to see; the `Interrupted` header comes from
  // the row's own status.
  it('keeps the saved content of a cancelled server call', () => {
    const stopped = call({
      status: 'cancelled',
      kind: 'other',
      title: 'mcp_server_lookup',
      rawInput: { toolName: 'lookup', providerIdentifier: 'server' },
    }, {
      rawOutput: { content: [{ type: 'tool-result', toolCallId: CALL, toolName: 'mcp_server_lookup', result: 'one hit so far' }] },
    })
    expect(stopped.kind).toBe('mcp')
    expect(isToolFailureResult(stopped.result)).toBe(false)
    expect(stopped.result).toMatchObject({ content: [{ type: 'text', text: 'one hit so far' }] })
  })

  // A call that ANSWERED keeps its saved content as the card, which is the whole
  // reason the restore exists.
  it('draws the saved content of a server call that answered', () => {
    const answered = call({
      status: 'completed',
      kind: 'other',
      title: 'mcp_server_lookup',
      rawInput: { toolName: 'lookup', providerIdentifier: 'server' },
    }, {
      rawOutput: { content: [{ type: 'tool-result', toolCallId: CALL, toolName: 'mcp_server_lookup', result: 'the server answered' }] },
    })
    expect(answered.kind).toBe('mcp')
    expect(answered.result).toMatchObject({ content: [{ type: 'text', text: 'the server answered' }] })
  })

  /*
   * A row the TURN retained still says `in_progress` in its own frame, and the
   * completion column is the only thing that says the call ended. The outcome test is
   * the shared ladder's -- the two words `failed` and `cancelled` -- and never
   * `status !== 'completed'`, which would read a retained row as a failure and throw
   * away the answer it did produce.
   */
  it('keeps the saved content of a retained server call the frame never completed', () => {
    const retained = acpToolCall({
      sessionUpdate: 'tool_call_update',
      toolCallId: CALL,
      status: 'in_progress',
      kind: 'other',
      title: 'mcp_server_lookup',
      rawInput: { toolName: 'lookup', providerIdentifier: 'server' },
    }, cursorToolCallAdapter, {
      sessionUpdate: 'tool_call_update',
      toolCallId: CALL,
      status: 'in_progress',
      rawOutput: { content: [{ type: 'tool-result', toolCallId: CALL, toolName: 'mcp_server_lookup', result: 'the server answered' }] },
    }, MessageCompletion.COMPLETE)
    expect(retained.kind).toBe('mcp')
    expect(retained.result).toMatchObject({ content: [{ type: 'text', text: 'the server answered' }] })
  })

  // A launch reports its own outcome, so it stays ABOVE the guard that stops the
  // restore of every other kind: the row states the run as failed and carries the
  // reason as its body, where falling through would draw the generic card instead.
  it('reports a failed subagent launch as a failed run', () => {
    const launch = call({
      status: 'failed',
      kind: 'other',
      title: 'Task: Inspect',
      rawInput: {},
      rawOutput: { error: 'the subagent could not start' },
    }, {
      rawOutput: { content: [{ type: 'tool-result', toolCallId: CALL, toolName: 'Task', result: '' }] },
    })
    expect(launch.kind).toBe('agent')
    const run = launch.kind === 'agent' ? typedResult(launch)?.agents[0] : undefined
    expect(run?.outcome).toBe('failed')
    expect(run?.body).toBe('the subagent could not start')
  })

  /**
   * An MCP frame whose body is its ACP content blocks, with no stored tool-result
   * beside it. The card has nothing else to draw.
   *
   * The shared build answers this row, and Cursor's branch keeps its result only when
   * that answer states `kind: 'mcp'`. It did not: no case list of the old
   * `acpSpecFor` switch held `mcp`, so the frame fell to the generic case and came back
   * as `kind: 'unspecified'` from behind a declared `ToolCallSpec<'mcp'>`. The test below
   * therefore matched nothing, `result` was always undefined, and the card drew empty.
   */
  it('draws the content blocks of an MCP call that saved no tool result', () => {
    const drawn = call({
      kind: 'other',
      title: 'mcp_server_lookup',
      rawInput: { toolName: 'lookup', providerIdentifier: 'server' },
      content: [{ type: 'content', content: { type: 'text', text: 'the server answered' } }],
    })
    expect(drawn.kind).toBe('mcp')
    expect(drawn.result).toMatchObject({ content: [{ type: 'text', text: 'the server answered' }] })
    // The call states its server and its tool, which the wire kind cannot.
    expect(drawn.request).toMatchObject({ server: 'server', tool: 'lookup' })
  })
})

describe('cursor stored tool records', () => {
  /** One saved tool result, as the worker stores it on the row. */
  function saved(toolName: string, result: unknown, success?: Record<string, unknown>) {
    return {
      rawOutput: {
        content: [{ type: 'tool-result', toolCallId: CALL, toolName, result }],
        ...(success ? { providerOptions: { cursor: { highLevelToolCallResult: { output: { success } } } } } : {}),
      },
    }
  }

  // `Delete` folded into `edit`, so a removed file drew the pencil and the word
  // "Edit". `deleteRenderer` exists, and the two request and result shapes are the
  // same pair the edit family uses.
  it('reads a removed file as a delete', () => {
    const removed = call(
      { kind: 'other', rawInput: { path: '/p/gone.ts' } },
      saved('Delete', 'Deleted', { path: '/p/gone.ts', diffString: '--- a/p/gone.ts\n+++ /dev/null\n@@ -1 +0,0 @@\n-gone\n' }),
    )
    expect(removed.kind).toBe('delete')
  })

  // Cursor answers in `rawOutput` and sends no Agent Client Protocol content block,
  // so the frame's own text is empty for most rows: a record this build does not
  // recognize had nothing at all to state.
  it('states the saved output of a record it does not recognize', () => {
    const unknown = call({ kind: 'other', rawInput: {} }, saved('SomethingNew', 'the tool said this'))
    expect(unknown.result).toMatchObject({ content: [{ type: 'text', text: 'the tool said this' }] })
  })
})

describe('cursor plans', () => {
  // The plan is the call's PROPOSAL, and `ReportRequest.proposal` is the typed field
  // for it. Burying it in the untyped payload bag put a Cursor parser in the shared
  // renderer, where no other provider could reach it.
  it('states the plan it proposed in the typed field', () => {
    const plan = call({
      kind: 'other',
      status: 'pending',
      sessionUpdate: 'tool_call',
      rawInput: { _toolName: 'createPlan', name: 'Rewrite the parser', plan: '# Step one' },
    })
    expect(plan.kind).toBe('report')
    expect(plan.kind === 'report' && plan.request.proposal).toBe('# Step one')
  })
})

/**
 * The question list one `askQuestion` row states, in Cursor's own key spellings.
 *
 * `questionsFromRecords` holds the control flow and the two invariants; `prompt` for the
 * question, and `id` for an option that states no label, are what stays here.
 */
describe('cursorToolCallAdapter question rows', () => {
  const questionsOf = (questions: unknown) => {
    const built = call({ kind: 'other', rawInput: { _toolName: 'askQuestion', questions } })
    return built.kind === 'question' ? built.request.questions : []
  }

  it('reads the prompt and the options one question offered', () => {
    expect(questionsOf([{ header: 'Layout', prompt: 'Choose a layout', options: [{ id: 'compact', label: 'Compact', description: 'Small' }] }]))
      .toStrictEqual([{ header: 'Layout', question: 'Choose a layout', options: [{ label: 'Compact', description: 'Small' }] }])
  })

  // Cursor sends an option with an id and no label, and the id is the word the runtime
  // itself shows there.
  it('labels an option with its id when it states no label', () => {
    expect(questionsOf([{ prompt: 'Choose a layout', options: [{ id: 'compact' }] }])[0]?.options)
      .toStrictEqual([{ label: 'compact' }])
  })

  it('drops a question that states no prompt', () => {
    expect(questionsOf([{ header: 'Layout', options: [{ id: 'compact' }] }])).toStrictEqual([])
  })

  it('drops an option that states neither a label nor an id', () => {
    expect(questionsOf([{ prompt: 'Choose a layout', options: [{ description: 'Small' }] }])[0]?.options).toStrictEqual([])
  })
})
