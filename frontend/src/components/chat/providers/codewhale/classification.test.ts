import type { MessageCategory } from '../../messageClassifier'
import { describe, expect, it } from 'vitest'
import { CODEWHALE_BLOCK_TYPE, CODEWHALE_EVENT, CODEWHALE_ITEM_KIND, CODEWHALE_TOOL, CODEWHALE_TRANSCRIPT_ROLE, CODEWHALE_TURN_STATUS } from '~/generated/contracts/codewhale-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'
import { classifyCodewhaleMessage } from './classification'
import { childBlock, codewhaleEvent, itemFinished, toolCompleted, toolStarted, turnCompleted } from './toolResults.fixtures'
import '~/components/chat/providers'

function classify(parent: Record<string, unknown> | undefined, wrapper?: { old_seqs: number[], messages: unknown[] }): MessageCategory {
  return classifyCodewhaleMessage(input(parent, wrapper, AgentProvider.CODEWHALE))
}

describe('classifyCodewhaleMessage', () => {
  it('reads a reply and a reasoning step, and hides one with no text', () => {
    expect(classify(itemFinished(CODEWHALE_ITEM_KIND.AgentMessage, 'Hello.'))).toStrictEqual({ kind: 'assistant_text' })
    expect(classify(itemFinished(CODEWHALE_ITEM_KIND.AgentReasoning, 'Hmm.'))).toStrictEqual({ kind: 'assistant_thinking' })
    expect(classify(itemFinished(CODEWHALE_ITEM_KIND.AgentMessage, '  '))).toStrictEqual({ kind: 'hidden' })
  })

  it('reads the two halves of a tool call', () => {
    expect(classify(toolStarted(CODEWHALE_TOOL.Bash, { command: 'ls' }))).toStrictEqual({ kind: 'tool_use' })
    expect(classify(toolCompleted(CODEWHALE_TOOL.Bash, { command: 'ls' }, 'a'))).toStrictEqual({ kind: 'tool_result' })
  })

  it('reads a retained opening frame as the result of its span', () => {
    const retained = { ...input(toolStarted(CODEWHALE_TOOL.Bash, {}), undefined, AgentProvider.CODEWHALE), completion: MessageCompletion.INTERRUPTED }
    expect(classifyCodewhaleMessage(retained)).toStrictEqual({ kind: 'tool_result' })
  })

  // Only a classified-hidden row leaves the transcript. A row that the extractor
  // empties still takes a slot of no height, which is never measured and holds every
  // later row behind it.
  it('hides the result row of a deferred tool\'s first call, which never ran', () => {
    expect(classify(toolCompleted(CODEWHALE_TOOL.ApplyPatch, { patch: 'x' }, 'Tool was deferred', { deferred_tool_loaded: true }))).toStrictEqual({ kind: 'hidden' })
    // Its request row cannot tell, and draws the call.
    expect(classify(toolStarted(CODEWHALE_TOOL.ApplyPatch, { patch: 'x' }))).toStrictEqual({ kind: 'tool_use' })
  })

  it('hides the result row of a subagent\'s deferred first call', () => {
    const loaded = childBlock(CODEWHALE_TRANSCRIPT_ROLE.User, { type: CODEWHALE_BLOCK_TYPE.ToolResult, tool_use_id: 'b1', content: 'Tool `grep_files` was deferred and has now been loaded. Retry the call with the newly available schema.' })
    expect(classify(loaded)).toStrictEqual({ kind: 'hidden' })
  })

  it('hides the redacted result row of an answered question, and keeps a failed one', () => {
    const answered = codewhaleEvent(CODEWHALE_EVENT.ItemCompleted, { item: { kind: CODEWHALE_ITEM_KIND.ToolCall, detail: 'User input submitted', metadata: { tool_call_id: 'q1', tool_name: CODEWHALE_TOOL.RequestUserInput, response_redacted: true } } })
    expect(classify(answered)).toStrictEqual({ kind: 'hidden' })
    const failed = codewhaleEvent(CODEWHALE_EVENT.ItemFailed, { item: { kind: CODEWHALE_ITEM_KIND.ToolCall, detail: 'Request cancelled while awaiting user input', metadata: { tool_call_id: 'q1', tool_name: CODEWHALE_TOOL.RequestUserInput, response_redacted: true } } })
    expect(classify(failed)).toStrictEqual({ kind: 'tool_result' })
    const stopped = codewhaleEvent(CODEWHALE_EVENT.ItemInterrupted, { item: { kind: CODEWHALE_ITEM_KIND.ToolCall, detail: 'Turn interrupted', metadata: { tool_call_id: 'q1', tool_name: CODEWHALE_TOOL.RequestUserInput, response_redacted: true } } })
    expect(classify(stopped)).toStrictEqual({ kind: 'tool_result' })
  })

  // The worker persists these four events as rows of their own. Each states
  // something the reader must know, so none may fall through to `unknown`.
  it('reads each runtime notice event as a notification', () => {
    for (const event of [CODEWHALE_EVENT.TurnSteerDropped, CODEWHALE_EVENT.ApprovalTimeout, CODEWHALE_EVENT.SandboxDenied, CODEWHALE_EVENT.StoreFailure])
      expect(classify(codewhaleEvent(event, {})).kind, event).toBe('notification')
  })

  it('reads an item event that carries no item as unknown', () => {
    expect(classify(codewhaleEvent(CODEWHALE_EVENT.ItemCompleted, {}))).toStrictEqual({ kind: 'unknown' })
    expect(classify(codewhaleEvent(CODEWHALE_EVENT.ItemFailed, { item: 'x' }))).toStrictEqual({ kind: 'unknown' })
  })

  it('reads a turn end', () => {
    expect(classify(turnCompleted(CODEWHALE_TURN_STATUS.Completed))).toStrictEqual({ kind: 'result_divider' })
  })

  it('reads the runtime\'s notices and notice items', () => {
    expect(classify(itemFinished(CODEWHALE_ITEM_KIND.Status, 'Checkpoint saved'))).toStrictEqual({ kind: 'notification', entries: [{ kind: 'status', text: 'Checkpoint saved' }] })
    expect(classify(itemFinished(CODEWHALE_ITEM_KIND.ContextCompaction, 'done', { auto: true }))).toStrictEqual({ kind: 'notification', entries: [{ kind: 'compaction', phase: 'end', detail: { trigger: 'auto' } }] })
    expect(classify(itemFinished(CODEWHALE_ITEM_KIND.Error, 'boom', {}, CODEWHALE_EVENT.ItemFailed))).toStrictEqual({ kind: 'notification', entries: [{ kind: 'text', text: 'Error: boom' }] })
    expect(classify(codewhaleEvent(CODEWHALE_EVENT.SandboxDenied, { tool_name: 'bash' }))).toStrictEqual({ kind: 'notification', entries: [{ kind: 'text', text: 'The sandbox denied bash' }] })
    expect(classify(itemFinished(CODEWHALE_ITEM_KIND.Status, ''))).toStrictEqual({ kind: 'hidden' })
  })

  it('hides the runtime\'s echo of a user message', () => {
    expect(classify(itemFinished(CODEWHALE_ITEM_KIND.UserMessage, 'Say hello.'))).toStrictEqual({ kind: 'hidden' })
  })

  it('reads the rows of a subagent\'s transcript', () => {
    expect(classify(childBlock(CODEWHALE_TRANSCRIPT_ROLE.Assistant, { type: CODEWHALE_BLOCK_TYPE.Text, text: 'Done.' }))).toStrictEqual({ kind: 'assistant_text' })
    expect(classify(childBlock(CODEWHALE_TRANSCRIPT_ROLE.Assistant, { type: CODEWHALE_BLOCK_TYPE.Thinking, thinking: 'Hmm.' }))).toStrictEqual({ kind: 'assistant_thinking' })
    expect(classify(childBlock(CODEWHALE_TRANSCRIPT_ROLE.Assistant, { type: CODEWHALE_BLOCK_TYPE.ToolUse, id: 'b1', name: 'read', input: {} }))).toStrictEqual({ kind: 'tool_use' })
    expect(classify(childBlock(CODEWHALE_TRANSCRIPT_ROLE.User, { type: CODEWHALE_BLOCK_TYPE.ToolResult, tool_use_id: 'b1', content: 'x' }))).toStrictEqual({ kind: 'tool_result' })
    expect(classify(childBlock(CODEWHALE_TRANSCRIPT_ROLE.Assistant, { type: 'image' }))).toStrictEqual({ kind: 'unknown' })
  })

  // The classifier is the one place that hides a row: an emptied row that the
  // extractor returned would still take a slot in the transcript.
  it('hides a subagent\'s text or thinking block that states nothing', () => {
    expect(classify(childBlock(CODEWHALE_TRANSCRIPT_ROLE.Assistant, { type: CODEWHALE_BLOCK_TYPE.Text, text: ' \n' }))).toStrictEqual({ kind: 'hidden' })
    expect(classify(childBlock(CODEWHALE_TRANSCRIPT_ROLE.Assistant, { type: CODEWHALE_BLOCK_TYPE.Thinking, thinking: '' }))).toStrictEqual({ kind: 'hidden' })
    expect(classify(itemFinished(CODEWHALE_ITEM_KIND.AgentReasoning, '\t'))).toStrictEqual({ kind: 'hidden' })
  })

  it('reads LeapMux\'s own rows', () => {
    expect(classify({ content: 'Say hello.' })).toStrictEqual({ kind: 'user_content' })
    expect(classify({ content: 'x', hidden: true })).toStrictEqual({ kind: 'hidden' })
    expect(classify({ content: 'x', planExecution: true })).toStrictEqual({ kind: 'plan_execution' })
    expect(classify({ type: 'interrupted' })).toMatchObject({ kind: 'notification' })
  })

  it('reads a notification thread by its entries, whatever each member states', () => {
    const thread = { old_seqs: [], messages: [itemFinished(CODEWHALE_ITEM_KIND.Status, 'one'), codewhaleEvent(CODEWHALE_EVENT.SandboxDenied, {})] }
    expect(classify(thread.messages[0] as Record<string, unknown>, thread)).toStrictEqual({ kind: 'notification', entries: [{ kind: 'status', text: 'one' }, { kind: 'text', text: 'The sandbox denied a tool' }] })
    expect(classify(undefined, { old_seqs: [], messages: [] })).toStrictEqual({ kind: 'hidden' })
    expect(classify({ event: 'a.later.event' }, { old_seqs: [], messages: [{ event: 'a.later.event' }] })).toStrictEqual({ kind: 'hidden' })
  })

  it('reads an unknown frame as unknown', () => {
    expect(classify(undefined)).toStrictEqual({ kind: 'unknown' })
    expect(classify(codewhaleEvent('a.later.event', {}))).toStrictEqual({ kind: 'unknown' })
    expect(classify(codewhaleEvent(CODEWHALE_EVENT.ItemCompleted, { item: { kind: 'a_later_kind', detail: 'x' } }))).toStrictEqual({ kind: 'unknown' })
    expect(classify({ type: 'a_later_type' })).toStrictEqual({ kind: 'unknown' })
  })
})
