import { describe, expect, it } from 'vitest'
import { ZCODE_EVENT, ZCODE_TOOL, ZCODE_TOOL_KIND } from '~/generated/contracts/zcode-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'
import { classifyZCodeMessage } from './classification'

/** Build a persisted native session event. */
function event(type: string, payload: Record<string, unknown> = {}): Record<string, unknown> {
  return { type, payload, sessionId: 's-1', seq: 1 }
}

/** A `tool.updated` row of the given lifecycle kind. */
function toolEvent(kind: string, payload: Record<string, unknown> = {}): Record<string, unknown> {
  return event(ZCODE_EVENT.ToolUpdated, { kind, toolCallId: 'call-1', ...payload })
}

describe('classifyZCodeMessage', () => {
  it('classifies a model-response session.updated with text as assistant_text', () => {
    const parent = event(ZCODE_EVENT.SessionUpdated, { content: 'hello', stopReason: 'stop' })
    expect(classifyZCodeMessage(input(parent))).toEqual({ kind: 'assistant_text' })
  })

  // A tool-only turn reports `content: ""` with a `tool-calls` stop reason. That is
  // the normal case, not a parse failure, and an empty bubble is worse than none.
  it('hides a model response whose content is empty', () => {
    const parent = event(ZCODE_EVENT.SessionUpdated, { content: '', stopReason: 'tool-calls' })
    expect(classifyZCodeMessage(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('hides a model response whose content is only whitespace', () => {
    const parent = event(ZCODE_EVENT.SessionUpdated, { content: '  \n ', stopReason: 'stop' })
    expect(classifyZCodeMessage(input(parent))).toEqual({ kind: 'hidden' })
  })

  // `session.updated` is the app-server's catch-all: request telemetry and
  // per-iteration counters arrive as the same type and carry no conversation.
  it('hides the telemetry variants of session.updated', () => {
    for (const payload of [
      { messageCount: 3, modelRef: { providerId: 'zai', modelId: 'glm-5.3' }, iteration: 2 },
      { usage: { inputTokens: 10, outputTokens: 2 }, contextWindow: 200000 },
      { content: 'no stop reason means this is not a finished generation' },
    ]) {
      expect(classifyZCodeMessage(input(event(ZCODE_EVENT.SessionUpdated, payload))))
        .toEqual({ kind: 'hidden' })
    }
  })

  // A background task belongs to the registry, which draws it as its own card.
  it('hides a background-task session.updated even when it carries text', () => {
    const parent = event(ZCODE_EVENT.SessionUpdated, {
      taskId: 'task-1',
      content: 'subagent said something',
      stopReason: 'stop',
    })
    expect(classifyZCodeMessage(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('classifies a scheduled tool.updated as tool_use carrying the tool name', () => {
    const parent = toolEvent(ZCODE_TOOL_KIND.Scheduled, {
      toolName: ZCODE_TOOL.Bash,
      input: { command: 'ls' },
    })
    expect(classifyZCodeMessage(input(parent))).toEqual({ kind: 'tool_use' })
  })

  // The worker backfills the input from the model stream, but a build that omits the
  // name entirely must still open a span rather than fall through to raw JSON.
  it('falls back to a generic tool name on a scheduled row that names none', () => {
    const result = classifyZCodeMessage(input(toolEvent(ZCODE_TOOL_KIND.Scheduled)))
    expect(result).toMatchObject({ kind: 'tool_use' })
  })

  it('classifies every finishing tool kind as tool_result', () => {
    for (const kind of [ZCODE_TOOL_KIND.Result, ZCODE_TOOL_KIND.Error, ZCODE_TOOL_KIND.Batch]) {
      expect(classifyZCodeMessage(input(toolEvent(kind))).kind).toBe('tool_result')
    }
  })

  // The Worker consumes `started` and `progress` for live counters. One
  // reaching a transcript means a provider changed its protocol.
  it('hides the mid-flight tool kinds', () => {
    for (const kind of [ZCODE_TOOL_KIND.Started, ZCODE_TOOL_KIND.Progress]) {
      expect(classifyZCodeMessage(input(toolEvent(kind)))).toEqual({ kind: 'hidden' })
    }
  })

  it('classifies both turn ends as a result divider', () => {
    for (const type of [ZCODE_EVENT.TurnCompleted, ZCODE_EVENT.TurnFailed]) {
      expect(classifyZCodeMessage(input(event(type)))).toEqual({ kind: 'result_divider' })
    }
  })

  // `session/stop` is answered with an empty object, and the app-server has been
  // observed to send no turn frame at all for the abort. The worker states that stop
  // in a LeapMux row, which carries no ZCode event for the dispatch above to match.
  it('classifies the stop row LeapMux writes as a notification', () => {
    expect(classifyZCodeMessage(input({ type: 'interrupted' })))
      .toEqual({ kind: 'notification', entries: [{ kind: 'text', text: 'Interrupted' }] })
  })

  it('classifies a resolved permission as a notification carrying the row', () => {
    const parent = event(ZCODE_EVENT.PermissionResolved, {
      decision: 'deny',
      toolName: ZCODE_TOOL.Bash,
    })
    const result = classifyZCodeMessage(input(parent))
    expect(result.kind).toBe('notification')
    if (result.kind === 'notification')
      expect(result.entries).toHaveLength(1)
  })

  it('classifies the steer and close notifications', () => {
    for (const type of [
      ZCODE_EVENT.TurnSteerQueued,
      ZCODE_EVENT.TurnSteerDrained,
      ZCODE_EVENT.SessionClosed,
    ]) {
      expect(classifyZCodeMessage(input(event(type))).kind).toBe('notification')
    }
  })

  // A permission.resolved the describer cannot read produces no line, so surfacing it
  // as a notification would render an empty row and fall back to raw JSON.
  it('hides a notification the describer cannot render', () => {
    const parent = { type: ZCODE_EVENT.PermissionResolved }
    expect(classifyZCodeMessage(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('hides every event type that carries no UI surface', () => {
    for (const type of [
      ZCODE_EVENT.SessionCreated,
      ZCODE_EVENT.SessionResumed,
      ZCODE_EVENT.SessionTitleUpdated,
      ZCODE_EVENT.TurnStarted,
      ZCODE_EVENT.MessageUpserted,
      ZCODE_EVENT.MessageRemoved,
      ZCODE_EVENT.PartStarted,
      ZCODE_EVENT.PartDelta,
      ZCODE_EVENT.PartUpserted,
      ZCODE_EVENT.PartRemoved,
      ZCODE_EVENT.ModelStreaming,
      ZCODE_EVENT.PermissionRequested,
      ZCODE_EVENT.UserInputRequested,
      ZCODE_EVENT.UserInputResolved,
      ZCODE_EVENT.CheckpointCreated,
      ZCODE_EVENT.RewindTriggered,
      ZCODE_EVENT.StreamRecoveryUpdated,
    ]) {
      expect(classifyZCodeMessage(input(event(type)))).toEqual({ kind: 'hidden' })
    }
  })

  // The service layer persists a user send as the LeapMux-neutral {content} shape,
  // with no ZCode `type`. It is matched before the event dispatch so the echo does not
  // land in the unknown fallback and get JSON-stringified into the bubble.
  it('classifies a neutral user row as user_content', () => {
    expect(classifyZCodeMessage(input({ content: 'do the thing' }))).toEqual({ kind: 'user_content' })
  })

  it('honours the neutral hidden and planExecution flags on a user row', () => {
    expect(classifyZCodeMessage(input({ content: 'x', hidden: true }))).toEqual({ kind: 'hidden' })
    expect(classifyZCodeMessage(input({ content: 'x', planExecution: true })))
      .toEqual({ kind: 'plan_execution' })
  })

  it('does not take the user path for a typed row that happens to carry content', () => {
    const parent = { type: ZCODE_EVENT.SessionUpdated, content: 'not the user', payload: {} }
    expect(classifyZCodeMessage(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('falls back to unknown for a row with no type and no content', () => {
    expect(classifyZCodeMessage(input({ somethingElse: 1 })).kind).toBe('unknown')
  })

  it('falls back to unknown for an event type ZCode does not have', () => {
    expect(classifyZCodeMessage(input(event('galaxy.exploded'))).kind).toBe('unknown')
  })

  it('returns unknown when there is no parent object at all', () => {
    expect(classifyZCodeMessage(input(undefined)).kind).toBe('unknown')
  })
})

describe('classifyZCodeMessage on consolidated notification threads', () => {
  it('classifies a multi-event wrapper as one notification carrying every message', () => {
    const messages = [
      event(ZCODE_EVENT.TurnSteerQueued, { inputPreview: 'also check the tests' }),
      event(ZCODE_EVENT.TurnSteerDrained),
    ]
    const result = classifyZCodeMessage(input(messages[0], { old_seqs: [], messages }))
    expect(result.kind).toBe('notification')
    if (result.kind === 'notification')
      expect(result.entries).toHaveLength(2)
  })

  it('drops the unrenderable entries from a thread but keeps the rest', () => {
    const blank = { type: ZCODE_EVENT.PermissionResolved }
    const steer = event(ZCODE_EVENT.TurnSteerDrained)
    const result = classifyZCodeMessage(input(blank, { old_seqs: [], messages: [blank, steer] }))
    expect(result.kind).toBe('notification')
    if (result.kind === 'notification')
      expect(result.entries).toHaveLength(1)
  })

  it('hides a thread whose every entry is unrenderable', () => {
    const messages = [{ type: ZCODE_EVENT.PermissionResolved }, { type: ZCODE_EVENT.PermissionResolved }]
    expect(classifyZCodeMessage(input(messages[0], { old_seqs: [], messages })))
      .toEqual({ kind: 'hidden' })
  })

  it('hides an empty wrapper', () => {
    expect(classifyZCodeMessage(input(undefined, { old_seqs: [], messages: [] })))
      .toEqual({ kind: 'hidden' })
  })

  // A wrapper whose entries are not notification types must fall through to the
  // per-message classification instead of being hijacked into the thread path.
  it('does not treat a wrapper of non-notification events as a thread', () => {
    const messages = [event(ZCODE_EVENT.SessionUpdated, { content: 'hi', stopReason: 'stop' })]
    expect(classifyZCodeMessage(input(messages[0], { old_seqs: [], messages })))
      .toEqual({ kind: 'assistant_text' })
  })
})

describe('classifyZCodeMessage on a retained tool row', () => {
  it.each([ZCODE_TOOL_KIND.Scheduled, ZCODE_TOOL_KIND.Started, ZCODE_TOOL_KIND.Progress])(
    'reads a retained %s row as the call result',
    (kind) => {
      const parent = toolEvent(kind, { stdoutTail: 'partial output' })
      expect(classifyZCodeMessage({ ...input(parent, null, AgentProvider.ZCODE), completion: MessageCompletion.ERROR }))
        .toEqual({ kind: 'tool_result' })
    },
  )
})
