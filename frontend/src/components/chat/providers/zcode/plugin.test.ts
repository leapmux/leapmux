import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { ZCODE_EVENT, ZCODE_MODE, ZCODE_TOOL, ZCODE_TOOL_KIND } from '~/generated/contracts/zcode-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { buildDenyResponse } from '~/utils/controlResponse'
import { renderDivider } from '../../messageRenderTestUtils'
import { providerFor } from '../registry'
import { input } from '../testUtils'

// Side-effect import to register the ZCode plugin.
import './plugin'

const plugin = providerFor(AgentProvider.ZCODE)!

/** One persisted ZCode row: the session-event envelope, which is what a row IS. */
function event(type: string, payload: Record<string, unknown> = {}): Record<string, unknown> {
  return { type, payload, sessionId: 's-1', seq: 1 }
}

/** A `tool.updated` row of the given lifecycle kind. */
function toolEvent(kind: string, payload: Record<string, unknown> = {}): Record<string, unknown> {
  return event(ZCODE_EVENT.ToolUpdated, { kind, toolCallId: 'call-1', ...payload })
}

function parsedOf(parent: Record<string, unknown>): ParsedMessageContent {
  return { rawText: '', topLevel: parent, parentObject: parent, wrapper: null }
}

describe('zcode plugin metadata', () => {
  it('is registered for the ZCODE provider', () => {
    expect(plugin).toBeDefined()
  })

  // Text is inlined into the prompt and an image rides `session/send.attachments`. A
  // PDF and a binary are refused: the app-server's normalizer has no PDF kind, so one
  // arrives as a generic file, is decoded as text when small and dropped when large.
  it('advertises text and image attachments only', () => {
    expect(plugin.attachments).toEqual({
      text: true,
      image: true,
      pdf: false,
      binary: false,
    })
  })

  it('carries its mode axis on the permission-mode channel', () => {
    expect(plugin.triggerModeGroupKey).toBe('permissionMode')
    expect(plugin.bypassSettings?.sets.permissionMode).toBe(ZCODE_MODE.Yolo)
  })

  it('configures plan mode against the same axis, defaulting to build', () => {
    expect(plugin.planMode).toMatchObject({
      groupKey: 'permissionMode',
      planValue: ZCODE_MODE.Plan,
      defaultValue: ZCODE_MODE.Build,
    })
  })

  it('reads the current plan-mode value from the agent option values', () => {
    const currentMode = plugin.planMode!.currentMode
    expect(currentMode({ optionValues: { permissionMode: ZCODE_MODE.Plan } } as never))
      .toBe(ZCODE_MODE.Plan)
  })

  it('falls back to build when the agent reports no permission mode yet', () => {
    const currentMode = plugin.planMode!.currentMode
    expect(currentMode({} as never)).toBe(ZCODE_MODE.Build)
    expect(currentMode({ optionValues: {} } as never)).toBe(ZCODE_MODE.Build)
  })

  // A ZCode session id is an opaque token the app-server mints, not a transcript
  // path -- so the UI must not try to render it as a file.
  it('does not treat the session id as a file path', () => {
    expect(plugin.sessionIdIsFilePath).toBeFalsy()
  })

  // Composer send is the reject path (the placeholder says so). Allow lives on its
  // own button. An empty send is still a deny -- otherwise Reject with an empty
  // editor is a silent no-op and the permission banner never leaves.
  it('builds a deny envelope for composer send, including an empty one', () => {
    expect(plugin.buildControlResponse!({}, '', 'req-1')).toEqual(buildDenyResponse('req-1', ''))
    expect(plugin.buildControlResponse!({}, 'do not', 'req-1')).toEqual(buildDenyResponse('req-1', 'do not'))
  })

  // The notification set is also the non-progress set: each of these is visible and
  // says nothing about the agent working, so the thinking heuristic scans past them.
  it('declares its notification types as non-progress', () => {
    expect([...plugin.nonProgressTypes!].sort()).toEqual([
      ZCODE_EVENT.PermissionResolved,
      ZCODE_EVENT.SessionClosed,
      ZCODE_EVENT.TurnSteerDrained,
      ZCODE_EVENT.TurnSteerQueued,
    ].sort())
  })
})

describe('zcode classify', () => {
  it('classifies a model-response session.updated with text as assistant_text', () => {
    const parent = event(ZCODE_EVENT.SessionUpdated, { content: 'hello', stopReason: 'stop' })
    expect(plugin.classify(input(parent))).toEqual({ kind: 'assistant_text' })
  })

  // A tool-only turn reports `content: ""` with a `tool-calls` stop reason. That is
  // the normal case, not a parse failure, and an empty bubble is worse than none.
  it('hides a model response whose content is empty', () => {
    const parent = event(ZCODE_EVENT.SessionUpdated, { content: '', stopReason: 'tool-calls' })
    expect(plugin.classify(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('hides a model response whose content is only whitespace', () => {
    const parent = event(ZCODE_EVENT.SessionUpdated, { content: '  \n ', stopReason: 'stop' })
    expect(plugin.classify(input(parent))).toEqual({ kind: 'hidden' })
  })

  // `session.updated` is the app-server's catch-all: request telemetry and
  // per-iteration counters arrive as the same type and carry no conversation.
  it('hides the telemetry variants of session.updated', () => {
    for (const payload of [
      { messageCount: 3, modelRef: { providerId: 'zai', modelId: 'glm-5.3' }, iteration: 2 },
      { usage: { inputTokens: 10, outputTokens: 2 }, contextWindow: 200000 },
      { content: 'no stop reason means this is not a finished generation' },
    ]) {
      expect(plugin.classify(input(event(ZCODE_EVENT.SessionUpdated, payload))))
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
    expect(plugin.classify(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('classifies a scheduled tool.updated as tool_use carrying the tool name', () => {
    const parent = toolEvent(ZCODE_TOOL_KIND.Scheduled, {
      toolName: ZCODE_TOOL.Bash,
      input: { command: 'ls' },
    })
    const result = plugin.classify(input(parent))
    expect(result.kind).toBe('tool_use')
    if (result.kind === 'tool_use') {
      expect(result.toolName).toBe(ZCODE_TOOL.Bash)
      expect(result.toolUse).toBe(parent)
      expect(result.content).toEqual([])
    }
  })

  // The worker backfills the input from the model stream, but a build that omits the
  // name entirely must still open a span rather than fall through to raw JSON.
  it('falls back to a generic tool name on a scheduled row that names none', () => {
    const result = plugin.classify(input(toolEvent(ZCODE_TOOL_KIND.Scheduled)))
    expect(result).toMatchObject({ kind: 'tool_use', toolName: 'tool' })
  })

  it('classifies every finishing tool kind as tool_result', () => {
    for (const kind of [ZCODE_TOOL_KIND.Result, ZCODE_TOOL_KIND.Error, ZCODE_TOOL_KIND.Batch]) {
      expect(plugin.classify(input(toolEvent(kind))).kind).toBe('tool_result')
    }
  })

  // `started` and `progress` are broadcast as stream chunks, never persisted. One
  // reaching a transcript means a build changed, and a raw JSON bubble mid-span is
  // worse than nothing.
  it('hides the mid-flight tool kinds', () => {
    for (const kind of [ZCODE_TOOL_KIND.Started, ZCODE_TOOL_KIND.Progress]) {
      expect(plugin.classify(input(toolEvent(kind)))).toEqual({ kind: 'hidden' })
    }
  })

  it('classifies both turn ends as a result divider', () => {
    for (const type of [ZCODE_EVENT.TurnCompleted, ZCODE_EVENT.TurnFailed]) {
      expect(plugin.classify(input(event(type)))).toEqual({ kind: 'result_divider' })
    }
  })

  it('classifies a resolved permission as a notification carrying the row', () => {
    const parent = event(ZCODE_EVENT.PermissionResolved, {
      decision: 'deny',
      toolName: ZCODE_TOOL.Bash,
    })
    expect(plugin.classify(input(parent))).toEqual({ kind: 'notification', messages: [parent] })
  })

  it('classifies the steer and close notifications', () => {
    for (const type of [
      ZCODE_EVENT.TurnSteerQueued,
      ZCODE_EVENT.TurnSteerDrained,
      ZCODE_EVENT.SessionClosed,
    ]) {
      expect(plugin.classify(input(event(type))).kind).toBe('notification')
    }
  })

  // A permission.resolved the describer cannot read produces no line, so surfacing it
  // as a notification would render an empty row and fall back to raw JSON.
  it('hides a notification the describer cannot render', () => {
    const parent = { type: ZCODE_EVENT.PermissionResolved }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'hidden' })
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
      expect(plugin.classify(input(event(type)))).toEqual({ kind: 'hidden' })
    }
  })

  // The service layer persists a user send as the LeapMux-neutral {content} shape,
  // with no ZCode `type`. It is matched before the event dispatch so the echo does not
  // land in the unknown fallback and get JSON-stringified into the bubble.
  it('classifies a neutral user row as user_content', () => {
    expect(plugin.classify(input({ content: 'do the thing' }))).toEqual({ kind: 'user_content' })
  })

  it('honours the neutral hidden and planExecution flags on a user row', () => {
    expect(plugin.classify(input({ content: 'x', hidden: true }))).toEqual({ kind: 'hidden' })
    expect(plugin.classify(input({ content: 'x', planExecution: true })))
      .toEqual({ kind: 'plan_execution' })
  })

  it('does not take the user path for a typed row that happens to carry content', () => {
    const parent = { type: ZCODE_EVENT.SessionUpdated, content: 'not the user', payload: {} }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('falls back to unknown for a row with no type and no content', () => {
    expect(plugin.classify(input({ somethingElse: 1 })).kind).toBe('unknown')
  })

  it('falls back to unknown for an event type ZCode does not have', () => {
    expect(plugin.classify(input(event('galaxy.exploded'))).kind).toBe('unknown')
  })

  it('returns unknown when there is no parent object at all', () => {
    expect(plugin.classify(input(undefined)).kind).toBe('unknown')
  })
})

describe('zcode classify of consolidated notification threads', () => {
  it('classifies a multi-event wrapper as one notification carrying every message', () => {
    const messages = [
      event(ZCODE_EVENT.TurnSteerQueued, { inputPreview: 'also check the tests' }),
      event(ZCODE_EVENT.TurnSteerDrained),
    ]
    expect(plugin.classify(input(messages[0], { old_seqs: [], messages })))
      .toEqual({ kind: 'notification', messages })
  })

  it('drops the unrenderable entries from a thread but keeps the rest', () => {
    const blank = { type: ZCODE_EVENT.PermissionResolved }
    const steer = event(ZCODE_EVENT.TurnSteerDrained)
    expect(plugin.classify(input(blank, { old_seqs: [], messages: [blank, steer] })))
      .toEqual({ kind: 'notification', messages: [steer] })
  })

  it('hides a thread whose every entry is unrenderable', () => {
    const messages = [{ type: ZCODE_EVENT.PermissionResolved }, { type: ZCODE_EVENT.PermissionResolved }]
    expect(plugin.classify(input(messages[0], { old_seqs: [], messages })))
      .toEqual({ kind: 'hidden' })
  })

  it('hides an empty wrapper', () => {
    expect(plugin.classify(input(undefined, { old_seqs: [], messages: [] })))
      .toEqual({ kind: 'hidden' })
  })

  // A wrapper whose entries are not notification types must fall through to the
  // per-message classification instead of being hijacked into the thread path.
  it('does not treat a wrapper of non-notification events as a thread', () => {
    const messages = [event(ZCODE_EVENT.SessionUpdated, { content: 'hi', stopReason: 'stop' })]
    expect(plugin.classify(input(messages[0], { old_seqs: [], messages })))
      .toEqual({ kind: 'assistant_text' })
  })
})

describe('zcode spanRole', () => {
  it('routes a scheduled row to opener and each finishing kind to result', () => {
    expect(plugin.spanRole!(parsedOf(toolEvent(ZCODE_TOOL_KIND.Scheduled)))).toBe('opener')
    for (const kind of [ZCODE_TOOL_KIND.Result, ZCODE_TOOL_KIND.Error, ZCODE_TOOL_KIND.Batch]) {
      expect(plugin.spanRole!(parsedOf(toolEvent(kind)))).toBe('result')
    }
  })

  it('reports other for a mid-flight kind and for a non-tool event', () => {
    expect(plugin.spanRole!(parsedOf(toolEvent(ZCODE_TOOL_KIND.Started)))).toBe('other')
    expect(plugin.spanRole!(parsedOf(event(ZCODE_EVENT.TurnCompleted)))).toBe('other')
  })

  it('reports other for a row with no parent object', () => {
    expect(plugin.spanRole!({ rawText: '', topLevel: null, parentObject: undefined, wrapper: null }))
      .toBe('other')
  })
})

describe('zcode resultDivider', () => {
  it('states the duration a completed turn reports', () => {
    expect(plugin.resultDivider!(event(ZCODE_EVENT.TurnCompleted, {
      resultType: 'success',
      duration: 1500,
    }))).toEqual({ label: 'Took 1.5s' })
  })

  it('says the turn ended when no duration arrived', () => {
    expect(plugin.resultDivider!(event(ZCODE_EVENT.TurnCompleted, { resultType: 'success' })))
      .toEqual({ label: 'Turn ended' })
  })

  it('marks a cancelled turn as an error, ignoring its duration', () => {
    expect(plugin.resultDivider!(event(ZCODE_EVENT.TurnCompleted, {
      resultType: 'cancelled',
      duration: 900,
    }))).toEqual({ label: 'Turn cancelled', isError: true })
  })

  it('states the code and the message of a failed turn', () => {
    expect(plugin.resultDivider!(event(ZCODE_EVENT.TurnFailed, {
      error: { code: 'provider_not_configured', message: 'no api key' },
    }))).toEqual({ label: 'Turn failed (provider_not_configured) — no api key', isError: true })
  })

  // `detail` is the app-server's long-form explanation (a provider response body, a
  // stack). It goes in the detail block so a multi-line value does not stretch the rule.
  it('puts the long-form detail in the detail field, not the label', () => {
    const model = plugin.resultDivider!(event(ZCODE_EVENT.TurnFailed, {
      error: { message: 'upstream refused', detail: 'HTTP 429\nretry-after: 30' },
    }))
    expect(model).toEqual({
      label: 'Turn failed — upstream refused',
      isError: true,
      detail: 'HTTP 429\nretry-after: 30',
    })
  })

  it('accepts an error that spells its code as a type', () => {
    expect(plugin.resultDivider!(event(ZCODE_EVENT.TurnFailed, { error: { type: 'overloaded' } })))
      .toEqual({ label: 'Turn failed (overloaded)', isError: true })
  })

  it('reports a bare failure when the error object is absent', () => {
    expect(plugin.resultDivider!(event(ZCODE_EVENT.TurnFailed)))
      .toEqual({ label: 'Turn failed', isError: true })
  })

  it('returns null for any other row', () => {
    expect(plugin.resultDivider!(event(ZCODE_EVENT.SessionUpdated))).toBeNull()
    expect(plugin.resultDivider!({ notAnEnvelope: true })).toBeNull()
  })

  it('draws a failed turn through the shared divider renderer end to end', () => {
    const { text, isError } = renderDivider(
      event(ZCODE_EVENT.TurnFailed, { error: { message: 'upstream refused' } }),
      AgentProvider.ZCODE,
    )
    expect(text).toContain('upstream refused')
    expect(isError).toBe(true)
  })
})

describe('zcode extractQuotableText', () => {
  it('quotes the assistant text of a model response', () => {
    const parsed = parsedOf(event(ZCODE_EVENT.SessionUpdated, {
      content: '  the answer  ',
      stopReason: 'stop',
    }))
    expect(plugin.extractQuotableText!({ kind: 'assistant_text' }, parsed)).toBe('the answer')
  })

  // `classify` never answers assistant_thinking for ZCode: reasoning arrives only as a
  // live `reasoning_delta` stream that the worker broadcasts and never persists.
  it('does not quote a thinking row, because ZCode never classifies one', () => {
    const parsed = parsedOf(event(ZCODE_EVENT.SessionUpdated, { content: 'reasoning', stopReason: 'stop' }))
    expect(plugin.extractQuotableText!({ kind: 'assistant_thinking' }, parsed)).toBeNull()
  })

  it('quotes a user row and a plan-execution row from the neutral content field', () => {
    const parsed = parsedOf({ content: ' do it ' })
    expect(plugin.extractQuotableText!({ kind: 'user_content' }, parsed)).toBe('do it')
    expect(plugin.extractQuotableText!({ kind: 'plan_execution' }, parsed)).toBe('do it')
  })

  it('returns null rather than an empty string for a blank body', () => {
    const parsed = parsedOf(event(ZCODE_EVENT.SessionUpdated, { content: '   ', stopReason: 'stop' }))
    expect(plugin.extractQuotableText!({ kind: 'assistant_text' }, parsed)).toBeNull()
    expect(plugin.extractQuotableText!({ kind: 'user_content' }, parsedOf({ content: '  ' }))).toBeNull()
  })

  it('returns null for a category that quotes nothing and for a missing parent', () => {
    expect(plugin.extractQuotableText!({ kind: 'tool_result' }, parsedOf(toolEvent(ZCODE_TOOL_KIND.Result))))
      .toBeNull()
    expect(plugin.extractQuotableText!(
      { kind: 'assistant_text' },
      { rawText: '', topLevel: null, parentObject: undefined, wrapper: null },
    )).toBeNull()
  })
})

describe('zcode contextUsageFromMessage', () => {
  const usageRow = (usage: Record<string, unknown>): ParsedMessageContent =>
    parsedOf(event(ZCODE_EVENT.SessionUpdated, { usage }))

  it('normalizes a usage snapshot onto the shared context-usage shape', () => {
    expect(plugin.contextUsageFromMessage!(usageRow({
      inputTokens: 100,
      outputTokens: 10,
      cacheReadTokens: 20,
      cacheWriteTokens: 5,
      totalTokens: 135,
    }))).toEqual({
      inputTokens: 100,
      outputTokens: 10,
      cacheReadInputTokens: 20,
      cacheCreationInputTokens: 5,
      contextTokens: 135,
    })
  })

  it('omits contextTokens when the snapshot reports no total', () => {
    expect(plugin.contextUsageFromMessage!(usageRow({ inputTokens: 100, outputTokens: 10 })))
      .toEqual({
        inputTokens: 100,
        outputTokens: 10,
        cacheReadInputTokens: 0,
        cacheCreationInputTokens: 0,
      })
  })

  it('returns null for an all-zero snapshot, which reports no usage at all', () => {
    expect(plugin.contextUsageFromMessage!(usageRow({
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      cacheWriteTokens: 0,
      totalTokens: 0,
    }))).toBeNull()
  })

  it('returns null for a session.updated with no usage and for another event type', () => {
    expect(plugin.contextUsageFromMessage!(parsedOf(event(ZCODE_EVENT.SessionUpdated, { messageCount: 3 }))))
      .toBeNull()
    expect(plugin.contextUsageFromMessage!(parsedOf(event(ZCODE_EVENT.TurnCompleted, {
      usage: { inputTokens: 10 },
    })))).toBeNull()
  })
})

describe('zcode toolResultMeta', () => {
  const resultRow = (result: Record<string, unknown>, extra: Record<string, unknown> = {}) =>
    toolEvent(ZCODE_TOOL_KIND.Result, { result, ...extra })

  it('returns null for a category that is not a tool result', () => {
    expect(plugin.toolResultMeta!({ kind: 'assistant_text' }, resultRow({ content: 'x' }), ZCODE_TOOL.Bash, undefined))
      .toBeNull()
  })

  it('returns null for a row that is not a tool.updated at all', () => {
    expect(plugin.toolResultMeta!({ kind: 'tool_result' }, event(ZCODE_EVENT.TurnCompleted), ZCODE_TOOL.Bash, undefined))
      .toBeNull()
  })

  it('reads a Bash result through the command-output path', () => {
    const output = 'one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\neleven'
    const meta = plugin.toolResultMeta!(
      { kind: 'tool_result' },
      resultRow({ content: output, perf: { detail: { kind: 'command', command: { exitCode: 0 } } } }),
      ZCODE_TOOL.Bash,
      undefined,
    )
    expect(meta).toMatchObject({ collapsible: true, hasDiff: false, hasCopyable: true })
    expect(meta?.copyableContent()).toBe(output)
  })

  it('marks a short Bash result uncollapsible and uncopyable when it is empty', () => {
    const meta = plugin.toolResultMeta!(
      { kind: 'tool_result' },
      resultRow({ content: '' }),
      ZCODE_TOOL.Bash,
      undefined,
    )
    expect(meta).toMatchObject({ collapsible: false, hasCopyable: false })
    expect(meta?.copyableContent()).toBeNull()
  })

  it('measures a Read result by its line count', () => {
    const numbered = Array.from({ length: 40 }, (_, i) => `${i + 1}\tline ${i + 1}`).join('\n')
    const meta = plugin.toolResultMeta!(
      { kind: 'tool_result' },
      resultRow({ content: numbered }),
      ZCODE_TOOL.Read,
      undefined,
    )
    expect(meta).toMatchObject({ collapsible: true, hasDiff: false, hasCopyable: true })
    expect(meta?.copyableContent()).toBe(numbered)
  })

  it('exposes the structured patch of an Edit result as a diff', () => {
    const meta = plugin.toolResultMeta!(
      { kind: 'tool_result' },
      resultRow({
        content: 'Edited.',
        display: {
          kind: 'file_diff',
          filePath: '/tmp/a.ts',
          structuredPatch: [{
            oldStart: 1,
            oldLines: 1,
            newStart: 1,
            newLines: 1,
            lines: ['-zcodeMetaOld', '+zcodeMetaNew'],
          }],
        },
      }),
      ZCODE_TOOL.Edit,
      undefined,
    )
    expect(meta).toMatchObject({ collapsible: false, hasDiff: true, hasCopyable: true })
    expect(meta?.copyableContent()).toContain('zcodeMetaNew')
  })

  // A failed edit renders its error text, not the edit it attempted -- otherwise the
  // toolbar would offer a split/unified toggle over a `<pre>` block.
  it('declares no diff for a failed Edit and copies the error text instead', () => {
    const meta = plugin.toolResultMeta!(
      { kind: 'tool_result' },
      toolEvent(ZCODE_TOOL_KIND.Error, { error: { message: 'old_string not found' } }),
      ZCODE_TOOL.Edit,
      undefined,
    )
    expect(meta).toMatchObject({ collapsible: false, hasDiff: false, hasCopyable: true })
    expect(meta?.copyableContent()).toBe('old_string not found')
  })

  // A result payload names no tool, so the paired scheduled row is what supplies the
  // Write body when the span type is absent too.
  it('reaches the paired scheduled row for a Write with no display', () => {
    const scheduled = toolEvent(ZCODE_TOOL_KIND.Scheduled, {
      toolName: ZCODE_TOOL.Write,
      input: { file_path: '/tmp/new.ts', content: 'zcodeMetaWriteBody\n' },
    })
    const meta = plugin.toolResultMeta!(
      { kind: 'tool_result' },
      resultRow({ content: 'Created.' }),
      undefined,
      parsedOf(scheduled),
    )
    expect(meta).toMatchObject({ hasDiff: true, hasCopyable: true })
    expect(meta?.copyableContent()).toContain('zcodeMetaWriteBody')
  })

  it('falls back to the plain result text for a tool with no dedicated reader', () => {
    const meta = plugin.toolResultMeta!(
      { kind: 'tool_result' },
      resultRow({ content: 'a/b.ts\nc/d.ts' }),
      ZCODE_TOOL.Glob,
      undefined,
    )
    expect(meta).toMatchObject({ collapsible: false, hasDiff: false, hasCopyable: true })
    expect(meta?.copyableContent()).toBe('a/b.ts\nc/d.ts')
  })
})

describe('zcode isAskUserQuestion', () => {
  it('recognizes the AskUserQuestion prompt by its recorded tool name', () => {
    expect(plugin.askUserQuestion!.isRequest({ request: { tool_name: ZCODE_TOOL.AskUserQuestion } })).toBe(true)
  })

  it('rejects the plan approval, which travels over the same method', () => {
    expect(plugin.askUserQuestion!.isRequest({ request: { tool_name: ZCODE_TOOL.ExitPlanMode } })).toBe(false)
  })

  it('rejects a permission request and an empty payload', () => {
    expect(plugin.askUserQuestion!.isRequest({ request: { tool_name: ZCODE_TOOL.Bash } })).toBe(false)
    expect(plugin.askUserQuestion!.isRequest({})).toBe(false)
  })
})

describe('zcode todo rows', () => {
  const opener = toolEvent(ZCODE_TOOL_KIND.Scheduled, {
    toolName: ZCODE_TOOL.TodoWrite,
    input: { todos: [{ content: 'A', status: 'pending', activeForm: 'Doing A' }] },
  })
  const result = toolEvent(ZCODE_TOOL_KIND.Result, { result: { success: true, content: 'ok' } })

  function classifyWithSpan(parent: Record<string, unknown>, spanType: string) {
    return plugin.classify({ ...input(parent, null, AgentProvider.ZCODE), spanType })
  }

  it('classifies the TodoWrite opener as a tool use', () => {
    const category = classifyWithSpan(opener, ZCODE_TOOL.TodoWrite)
    expect(category.kind).toBe('tool_use')
    expect(category.kind === 'tool_use' && category.toolName).toBe(ZCODE_TOOL.TodoWrite)
  })

  // The opener draws the list itself, so the result row would only repeat it. A
  // result payload names no tool, which is why the span type answers instead.
  it('hides the TodoWrite result row', () => {
    expect(classifyWithSpan(result, ZCODE_TOOL.TodoWrite).kind).toBe('hidden')
  })

  it('keeps every other tool result visible', () => {
    expect(classifyWithSpan(result, ZCODE_TOOL.Bash).kind).toBe('tool_result')
  })
})
