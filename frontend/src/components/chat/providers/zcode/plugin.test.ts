import type { MessageCategory } from '../../messageClassification'
import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import { describe, expect, it } from 'vitest'
import { ZCODE_EVENT, ZCODE_MODE, ZCODE_TOOL, ZCODE_TOOL_KIND } from '~/generated/contracts/zcode-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { renderDivider } from '~/test-support/messageRenderProbes'
import { providerQuotableText, providerToolMeta } from '~/test-support/toolCallIr'
import { buildDenyResponse } from '~/utils/controlResponse'
import { toolCallMeta } from '../../results/tools/meta'
import { extractChatRow, extractedRow } from '../../rowExtraction'
import { providerFor, resolveMessageForRendering } from '../registry'
import { input } from '../testUtils'

// Side-effect import to register the ZCode plugin.
import './plugin'

const plugin = providerFor(AgentProvider.ZCODE)!

/** Build a persisted native session event. */
function event(type: string, payload: Record<string, unknown> = {}): Record<string, unknown> {
  return { type, payload, sessionId: 's-1', seq: 1 }
}

/** A `tool.updated` row of the given lifecycle kind. */
function toolEvent(kind: string, payload: Record<string, unknown> = {}): Record<string, unknown> {
  return event(ZCODE_EVENT.ToolUpdated, { kind, toolCallId: 'call-1', ...payload })
}

function parsedOf(parent: Record<string, unknown>): ResolvedMessageContent {
  return resolveMessageForRendering({ rawText: '', topLevel: parent, parentObject: parent, wrapper: null }, AgentProvider.ZCODE)
}

describe('zcode plugin metadata', () => {
  it('is registered for the ZCODE provider', () => {
    expect(plugin).toBeDefined()
  })

  // Text is inlined into the prompt and an image rides `session/send.attachments`. A
  // PDF and a binary are refused: the app-server's normalizer has no PDF kind, so one
  // arrives as a generic file, is decoded as text when small and dropped when large.
  it('advertises text and image attachments only', () => {
    expect(plugin?.configuration?.attachments).toEqual({
      text: true,
      image: true,
      pdf: false,
      binary: false,
    })
  })

  it('carries its mode axis on the permission-mode channel', () => {
    expect(plugin?.configuration?.triggerModeGroupKey).toBe('permissionMode')
    expect(plugin?.controls?.permissionPresets).toEqual({
      bypass: { sets: { permissionMode: ZCODE_MODE.Yolo } },
    })
  })

  it('configures plan mode against the same axis, defaulting to build', () => {
    expect(plugin?.configuration?.planMode).toMatchObject({
      groupKey: 'permissionMode',
      planValue: ZCODE_MODE.Plan,
      defaultValue: ZCODE_MODE.Build,
    })
  })

  it('reads the current plan-mode value from the agent option values', () => {
    const currentMode = plugin!.configuration!.planMode!.currentMode
    expect(currentMode({ optionValues: { permissionMode: ZCODE_MODE.Plan } } as never))
      .toBe(ZCODE_MODE.Plan)
  })

  it('falls back to build when the agent reports no permission mode yet', () => {
    const currentMode = plugin!.configuration!.planMode!.currentMode
    expect(currentMode({} as never)).toBe(ZCODE_MODE.Build)
    expect(currentMode({ optionValues: {} } as never)).toBe(ZCODE_MODE.Build)
  })

  // A ZCode session id is an opaque token the app-server mints, not a transcript
  // path -- so the UI must not try to render it as a file.
  it('does not treat the session id as a file path', () => {
    expect(plugin?.session?.sessionIdIsFilePath).toBeFalsy()
  })

  // Composer send is the reject path (the placeholder says so). Allow lives on its
  // own button. An empty send is still a deny -- otherwise Reject with an empty
  // editor is a silent no-op and the permission banner never leaves.
  it('builds a deny envelope for composer send, including an empty one', () => {
    expect(plugin?.controls?.buildControlResponse!({}, '', 'req-1')).toEqual(buildDenyResponse('req-1', ''))
    expect(plugin?.controls?.buildControlResponse!({}, 'do not', 'req-1')).toEqual(buildDenyResponse('req-1', 'do not'))
  })
})

describe('zcode spanRole', () => {
  it('routes a scheduled row to opener and each finishing kind to result', () => {
    expect(plugin?.transcript.spanRole!(parsedOf(toolEvent(ZCODE_TOOL_KIND.Scheduled)))).toBe('request')
    for (const kind of [ZCODE_TOOL_KIND.Result, ZCODE_TOOL_KIND.Error, ZCODE_TOOL_KIND.Batch]) {
      expect(plugin?.transcript.spanRole!(parsedOf(toolEvent(kind)))).toBe('result')
    }
  })

  it('reports other for a mid-flight kind and for a non-tool event', () => {
    expect(plugin?.transcript.spanRole!(parsedOf(toolEvent(ZCODE_TOOL_KIND.Started)))).toBe('other')
    expect(plugin?.transcript.spanRole!(parsedOf(event(ZCODE_EVENT.TurnCompleted)))).toBe('other')
  })

  it('reports other for a row with no parent object', () => {
    expect(plugin?.transcript.spanRole!(resolveMessageForRendering({ rawText: '', topLevel: null, parentObject: undefined, wrapper: null }, AgentProvider.ZCODE)))
      .toBe('other')
  })

  // A turn that ends while a call runs stores the agent's own LAST frame. That frame
  // is a progress or scheduled kind, and LeapMux's completion column is what states
  // that the call did not finish, so the retained row is the call's result.
  it.each([ZCODE_TOOL_KIND.Scheduled, ZCODE_TOOL_KIND.Started, ZCODE_TOOL_KIND.Progress])(
    'routes a retained %s row to result',
    (kind) => {
      const parsed = resolveMessageForRendering({ ...parsedOf(toolEvent(kind)), completion: MessageCompletion.INTERRUPTED }, AgentProvider.ZCODE)
      expect(plugin?.transcript.spanRole!(parsed)).toBe('result')
    },
  )
})

describe('zcode retained tool row', () => {
  // The two tails are SEPARATE streams, and the app-server cuts each at a byte count
  // rather than at a line end. Concatenating them glues a mid-line stdout tail to the
  // first stderr line and shows one line that neither stream wrote.
  //
  // The row is read through `extractChatRow` rather than through `providerToolMeta`,
  // which builds its parsed message from the frame alone. Only LeapMux's completion
  // column states that this call ended: the progress frame still reads as a call in
  // flight, and a call in flight carries no result at all -- `ToolMessage` draws the
  // live output the worker broadcasts there.
  it('presents the progress output tails as the partial result, one stream per line', () => {
    const parsed = resolveMessageForRendering({
      ...parsedOf(toolEvent(ZCODE_TOOL_KIND.Progress, { stdoutTail: 'partial ', stderrTail: 'output' })),
      completion: MessageCompletion.INTERRUPTED,
    }, AgentProvider.ZCODE)
    const row = extractedRow(extractChatRow(AgentProvider.ZCODE, parsed, { kind: 'tool_result' }, {
      spanType: ZCODE_TOOL.Bash,
      sides: { current: parsed, request: undefined, result: undefined, role: 'result' },
    }))
    expect(row?.kind === 'tool' ? toolCallMeta(row).copyableContent() : null).toBe('partial \noutput')
  })
})

describe('zcode resultDivider', () => {
  // Every provider states a turn end in one shared vocabulary. ZCode said "Took 1.5s"
  // where the Agent Client Protocol providers said "Turn ended".
  it('states the duration a completed turn reports', () => {
    expect(plugin?.transcript.extractDivider!(event(ZCODE_EVENT.TurnCompleted, {
      resultType: 'success',
      duration: 1500,
    }))).toEqual({ label: 'Turn ended (1.5s)' })
  })

  it('says the turn ended when no duration arrived', () => {
    expect(plugin?.transcript.extractDivider!(event(ZCODE_EVENT.TurnCompleted, { resultType: 'success' })))
      .toEqual({ label: 'Turn ended' })
  })

  // A cancelled turn is not an error: the reader asked for it, and the duration is
  // still what the turn took.
  it('states a cancelled turn as an interruption, with its duration', () => {
    expect(plugin?.transcript.extractDivider!(event(ZCODE_EVENT.TurnCompleted, {
      resultType: 'cancelled',
      duration: 900,
    }))).toEqual({ label: 'Turn interrupted (900ms)' })
  })

  it('states the code and the message of a failed turn', () => {
    expect(plugin?.transcript.extractDivider!(event(ZCODE_EVENT.TurnFailed, {
      error: { code: 'provider_not_configured', message: 'no api key' },
    }))).toEqual({ label: 'Turn failed (provider_not_configured) — no api key', isError: true })
  })

  // `detail` is the app-server's long-form explanation (a provider response body, a
  // stack). It goes in the detail block so a multi-line value does not stretch the rule.
  it('puts the long-form detail in the detail field, not the label', () => {
    const model = plugin?.transcript.extractDivider!(event(ZCODE_EVENT.TurnFailed, {
      error: { message: 'upstream refused', detail: 'HTTP 429\nretry-after: 30' },
    }))
    expect(model).toEqual({
      label: 'Turn failed — upstream refused',
      isError: true,
      detail: 'HTTP 429\nretry-after: 30',
    })
  })

  it('accepts an error that spells its code as a type', () => {
    expect(plugin?.transcript.extractDivider!(event(ZCODE_EVENT.TurnFailed, { error: { type: 'overloaded' } })))
      .toEqual({ label: 'Turn failed (overloaded)', isError: true })
  })

  it('reports a bare failure when the error object is absent', () => {
    expect(plugin?.transcript.extractDivider!(event(ZCODE_EVENT.TurnFailed)))
      .toEqual({ label: 'Turn failed', isError: true })
  })

  it('returns null for any other row', () => {
    expect(plugin?.transcript.extractDivider!(event(ZCODE_EVENT.SessionUpdated))).toBeNull()
    expect(plugin?.transcript.extractDivider!({ notAnEnvelope: true })).toBeNull()
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

describe('zcode quotable text', () => {
  const quotable = (payload: Record<string, unknown>, category: MessageCategory) =>
    providerQuotableText(AgentProvider.ZCODE, payload, { category })

  it('quotes the assistant text of a model response', () => {
    expect(quotable(event(ZCODE_EVENT.SessionUpdated, { content: '  the answer  ', stopReason: 'stop' }), { kind: 'assistant_text' }))
      .toBe('the answer')
  })

  // Native ZCode messages never classify as assistant_thinking. Worker-assembled
  // reasoning uses the shared classifier before this plugin runs.
  it('does not quote a native message as thinking', () => {
    expect(quotable(event(ZCODE_EVENT.SessionUpdated, { content: 'reasoning', stopReason: 'stop' }), { kind: 'assistant_thinking' }))
      .toBeNull()
  })

  it('quotes a user row and a plan-execution row from the neutral content field', () => {
    expect(quotable({ content: ' do it ' }, { kind: 'user_content' })).toBe('do it')
    expect(quotable({ content: ' do it ' }, { kind: 'plan_execution' })).toBe('do it')
  })

  it('returns null rather than an empty string for a blank body', () => {
    expect(quotable(event(ZCODE_EVENT.SessionUpdated, { content: '   ', stopReason: 'stop' }), { kind: 'assistant_text' })).toBeNull()
    expect(quotable({ content: '  ' }, { kind: 'user_content' })).toBeNull()
  })

  it('returns null for a category that quotes nothing', () => {
    expect(quotable(toolEvent(ZCODE_TOOL_KIND.Result), { kind: 'tool_result' })).toBeNull()
  })
})

describe('zcode contextUsageFromMessage', () => {
  const usageRow = (usage: Record<string, unknown>): ResolvedMessageContent =>
    parsedOf(event(ZCODE_EVENT.SessionUpdated, { usage }))

  it('normalizes a usage snapshot onto the shared context-usage shape', () => {
    expect(plugin?.session?.contextUsageFromMessage!(usageRow({
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
    expect(plugin?.session?.contextUsageFromMessage!(usageRow({ inputTokens: 100, outputTokens: 10 })))
      .toEqual({
        inputTokens: 100,
        outputTokens: 10,
        cacheReadInputTokens: 0,
        cacheCreationInputTokens: 0,
      })
  })

  it('returns null for an all-zero snapshot, which reports no usage at all', () => {
    expect(plugin?.session?.contextUsageFromMessage!(usageRow({
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      cacheWriteTokens: 0,
      totalTokens: 0,
    }))).toBeNull()
  })

  it('returns null for a session.updated with no usage and for another event type', () => {
    expect(plugin?.session?.contextUsageFromMessage!(parsedOf(event(ZCODE_EVENT.SessionUpdated, { messageCount: 3 }))))
      .toBeNull()
    expect(plugin?.session?.contextUsageFromMessage!(parsedOf(event(ZCODE_EVENT.TurnCompleted, {
      usage: { inputTokens: 10 },
    })))).toBeNull()
  })
})

describe('zcode tool row toolbar metadata', () => {
  const resultRow = (result: Record<string, unknown>, extra: Record<string, unknown> = {}) =>
    toolEvent(ZCODE_TOOL_KIND.Result, { result, ...extra })

  it('returns null for a category that is not a tool result', () => {
    expect(providerToolMeta(AgentProvider.ZCODE, resultRow({ content: 'x' }), { category: { kind: 'assistant_text' }, spanType: ZCODE_TOOL.Bash }))
      .toBeNull()
  })

  it('returns null for a row that is not a tool.updated at all', () => {
    expect(providerToolMeta(AgentProvider.ZCODE, event(ZCODE_EVENT.TurnCompleted), { category: { kind: 'tool_result' }, spanType: ZCODE_TOOL.Bash }))
      .toBeNull()
  })

  it('reads a Bash result through the command-output path', () => {
    const output = 'one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\neleven'
    const meta = providerToolMeta(AgentProvider.ZCODE, resultRow({ content: output, perf: { detail: { kind: 'command', command: { exitCode: 0 } } } }), { category: { kind: 'tool_result' }, spanType: ZCODE_TOOL.Bash })
    expect(meta).toMatchObject({ collapsible: true, hasDiff: false, hasCopyable: true })
    expect(meta?.copyableContent()).toBe(output)
  })

  it('marks a short Bash result uncollapsible and uncopyable when it is empty', () => {
    const meta = providerToolMeta(AgentProvider.ZCODE, resultRow({ content: '' }), { category: { kind: 'tool_result' }, spanType: ZCODE_TOOL.Bash })
    expect(meta).toMatchObject({ collapsible: false, hasCopyable: false })
    expect(meta?.copyableContent()).toBeNull()
  })

  it('measures a Read result by its line count', () => {
    const numbered = Array.from({ length: 40 }, (_, i) => `${i + 1}\tline ${i + 1}`).join('\n')
    const meta = providerToolMeta(AgentProvider.ZCODE, resultRow({ content: numbered }), { category: { kind: 'tool_result' }, spanType: ZCODE_TOOL.Read })
    expect(meta).toMatchObject({ collapsible: true, hasDiff: false, hasCopyable: true })
    expect(meta?.copyableContent()).toBe(Array.from({ length: 40 }, (_, i) => `line ${i + 1}`).join('\n'))
  })

  it('exposes the structured patch of an Edit result as a diff', () => {
    const meta = providerToolMeta(AgentProvider.ZCODE, resultRow({
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
    }), { category: { kind: 'tool_result' }, spanType: ZCODE_TOOL.Edit })
    expect(meta).toMatchObject({ collapsible: false, hasDiff: true, hasCopyable: true })
    expect(meta?.copyableContent()).toContain('zcodeMetaNew')
  })

  // A failed edit renders its error text, not the edit it attempted -- otherwise the
  // toolbar would offer a split/unified toggle over a `<pre>` block.
  it('declares no diff for a failed Edit and copies the error text instead', () => {
    const meta = providerToolMeta(AgentProvider.ZCODE, toolEvent(ZCODE_TOOL_KIND.Error, { error: { message: 'old_string not found' } }), { category: { kind: 'tool_result' }, spanType: ZCODE_TOOL.Edit })
    expect(meta).toMatchObject({ collapsible: false, hasDiff: false, hasCopyable: true })
    expect(meta?.copyableContent()).toBe('old_string not found')
  })

  // A result payload states no tool, so the paired scheduled row is what supplies the
  // Write body when the span type is absent too.
  it('reaches the paired scheduled row for a Write with no display', () => {
    const scheduled = toolEvent(ZCODE_TOOL_KIND.Scheduled, {
      toolName: ZCODE_TOOL.Write,
      input: { file_path: '/tmp/new.ts', content: 'zcodeMetaWriteBody\n' },
    })
    const meta = providerToolMeta(AgentProvider.ZCODE, resultRow({ content: 'Created.' }), { category: { kind: 'tool_result' }, request: parsedOf(scheduled) })
    expect(meta).toMatchObject({ hasDiff: true, hasCopyable: true })
    expect(meta?.copyableContent()).toContain('zcodeMetaWriteBody')
  })

  it('falls back to the plain result text for a tool with no dedicated reader', () => {
    const meta = providerToolMeta(AgentProvider.ZCODE, resultRow({ content: 'a/b.ts\nc/d.ts' }), { category: { kind: 'tool_result' }, spanType: ZCODE_TOOL.Glob })
    expect(meta).toMatchObject({ collapsible: false, hasDiff: false, hasCopyable: true })
    expect(meta?.copyableContent()).toBe('a/b.ts\nc/d.ts')
  })
})

describe('zcode isAskUserQuestion', () => {
  it('recognizes the AskUserQuestion prompt by its recorded tool name', () => {
    expect(plugin?.controls?.askUserQuestion!.isRequest({ request: { tool_name: ZCODE_TOOL.AskUserQuestion } })).toBe(true)
  })

  it('rejects the plan approval, which travels over the same method', () => {
    expect(plugin?.controls?.askUserQuestion!.isRequest({ request: { tool_name: ZCODE_TOOL.ExitPlanMode } })).toBe(false)
  })

  it('rejects a permission request and an empty payload', () => {
    expect(plugin?.controls?.askUserQuestion!.isRequest({ request: { tool_name: ZCODE_TOOL.Bash } })).toBe(false)
    expect(plugin?.controls?.askUserQuestion!.isRequest({})).toBe(false)
  })
})

describe('zcode todo rows', () => {
  const opener = toolEvent(ZCODE_TOOL_KIND.Scheduled, {
    toolName: ZCODE_TOOL.TodoWrite,
    input: { todos: [{ content: 'A', status: 'pending', activeForm: 'Doing A' }] },
  })
  const result = toolEvent(ZCODE_TOOL_KIND.Result, { result: { success: true, content: 'ok' } })

  function classifyWithSpan(parent: Record<string, unknown>, spanType: string) {
    return plugin?.transcript.classify({ ...input(parent, null, AgentProvider.ZCODE), spanType })
  }

  it('classifies the TodoWrite opener as a tool use', () => {
    expect(classifyWithSpan(opener, ZCODE_TOOL.TodoWrite).kind).toBe('tool_use')
  })

  it('keeps the TodoWrite result row for the shared checklist', () => {
    expect(classifyWithSpan(result, ZCODE_TOOL.TodoWrite).kind).toBe('tool_result')
  })

  it('keeps every other tool result visible', () => {
    expect(classifyWithSpan(result, ZCODE_TOOL.Bash).kind).toBe('tool_result')
  })
})
