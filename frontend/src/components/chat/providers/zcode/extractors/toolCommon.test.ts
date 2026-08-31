import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { ZCODE_EVENT, ZCODE_TOOL, ZCODE_TOOL_KIND } from '~/generated/contracts/zcode-protocol'
import {
  zcodeEnvelope,
  zcodeErrorText,
  zcodeExtractTool,
  zcodeRow,
  zcodeTodoListFromInput,
  zcodeToolInput,
} from './toolCommon'

function toolEvent(kind: string, payload: Record<string, unknown> = {}): Record<string, unknown> {
  return { type: ZCODE_EVENT.ToolUpdated, payload: { kind, toolCallId: 'call-1', ...payload } }
}

function parsedOf(parent: Record<string, unknown>): ParsedMessageContent {
  return { rawText: '', topLevel: parent, parentObject: parent, wrapper: null }
}

describe('zcodeEnvelope', () => {
  it('unwraps a row into its type and payload', () => {
    expect(zcodeEnvelope({ type: ZCODE_EVENT.TurnCompleted, payload: { duration: 5 } }))
      .toEqual({ type: ZCODE_EVENT.TurnCompleted, payload: { duration: 5 } })
  })

  it('reports an empty payload for a row that carries none', () => {
    expect(zcodeEnvelope({ type: ZCODE_EVENT.TurnCompleted }))
      .toEqual({ type: ZCODE_EVENT.TurnCompleted, payload: {} })
    expect(zcodeEnvelope({ type: ZCODE_EVENT.TurnCompleted, payload: 'not an object' }))
      .toEqual({ type: ZCODE_EVENT.TurnCompleted, payload: {} })
  })

  it('returns null for anything that is not a typed row', () => {
    expect(zcodeEnvelope({ payload: {} })).toBeNull()
    expect(zcodeEnvelope({ type: '' })).toBeNull()
    expect(zcodeEnvelope({ type: 7 })).toBeNull()
    expect(zcodeEnvelope(null)).toBeNull()
    expect(zcodeEnvelope('a string')).toBeNull()
    expect(zcodeEnvelope(undefined)).toBeNull()
  })
})

describe('zcodeExtractTool', () => {
  it('normalizes a scheduled row', () => {
    expect(zcodeExtractTool(toolEvent(ZCODE_TOOL_KIND.Scheduled, {
      toolName: ZCODE_TOOL.Bash,
      input: { command: 'ls' },
    }))).toEqual({
      kind: ZCODE_TOOL_KIND.Scheduled,
      toolCallId: 'call-1',
      toolName: ZCODE_TOOL.Bash,
      input: { command: 'ls' },
      result: null,
      error: null,
      isError: false,
      durationMs: null,
    })
  })

  it('normalizes a result row, which names no tool of its own', () => {
    const update = zcodeExtractTool(toolEvent(ZCODE_TOOL_KIND.Result, {
      duration: 90,
      result: {
        content: 'done',
        truncated: true,
        originalBytes: 4096,
        returnedBytes: 1024,
        display: { kind: 'file_diff' },
        perf: { detail: { kind: 'command' } },
      },
    }))
    expect(update).toMatchObject({ toolName: '', durationMs: 90, isError: false })
    expect(update?.result).toEqual({
      success: true,
      content: 'done',
      display: { kind: 'file_diff' },
      perfDetail: { kind: 'command' },
      truncated: true,
      originalBytes: 4096,
      returnedBytes: 1024,
    })
  })

  // The app-server omits `success` on some result shapes; an absent flag with a
  // present result means it succeeded, so only an explicit false is a failure.
  it('treats an absent success flag as success and an explicit false as failure', () => {
    expect(zcodeExtractTool(toolEvent(ZCODE_TOOL_KIND.Result, { result: { content: 'x' } }))?.result?.success)
      .toBe(true)
    expect(zcodeExtractTool(toolEvent(ZCODE_TOOL_KIND.Result, { result: { success: false } }))?.isError)
      .toBe(true)
  })

  // Three independent signals, and any one of them is a failure.
  it('flags a failure from the kind, from an error object, and from an unsuccessful result', () => {
    expect(zcodeExtractTool(toolEvent(ZCODE_TOOL_KIND.Error))?.isError).toBe(true)
    expect(zcodeExtractTool(toolEvent(ZCODE_TOOL_KIND.Result, { error: { message: 'no' } }))?.isError).toBe(true)
    expect(zcodeExtractTool(toolEvent(ZCODE_TOOL_KIND.Result, { result: { success: false } }))?.isError).toBe(true)
  })

  it('returns null for another event type and for a row with no toolCallId', () => {
    expect(zcodeExtractTool({ type: ZCODE_EVENT.SessionUpdated, payload: { kind: 'result' } })).toBeNull()
    expect(zcodeExtractTool({ type: ZCODE_EVENT.ToolUpdated, payload: { kind: 'result' } })).toBeNull()
    expect(zcodeExtractTool(null)).toBeNull()
  })

  // The unwrap is memoized by payload identity so a render pass does not walk the
  // same row repeatedly. The cached value must be the same object, not a copy.
  it('returns the identical object for a repeated call on the same row', () => {
    const row = toolEvent(ZCODE_TOOL_KIND.Result, { result: { content: 'x' } })
    expect(zcodeExtractTool(row)).toBe(zcodeExtractTool(row))
  })

  it('caches a null result too, rather than re-deriving it', () => {
    const row = { type: ZCODE_EVENT.SessionUpdated, payload: {} }
    expect(zcodeExtractTool(row)).toBeNull()
    expect(zcodeExtractTool(row)).toBeNull()
  })
})

describe('zcodeToolName', () => {
  const result = toolEvent(ZCODE_TOOL_KIND.Result, { result: { content: 'x' } })

  it('prefers the payload name, which is authoritative when present', () => {
    const row = toolEvent(ZCODE_TOOL_KIND.Scheduled, { toolName: ZCODE_TOOL.Bash })
    expect(zcodeRow(row, ZCODE_TOOL.Read, parsedOf(toolEvent(ZCODE_TOOL_KIND.Scheduled, {
      toolName: ZCODE_TOOL.Glob,
    }))).toolName).toBe(ZCODE_TOOL.Bash)
  })

  it('falls back to the span type, which every span row carries', () => {
    expect(zcodeRow(result, ZCODE_TOOL.Read, undefined).toolName).toBe(ZCODE_TOOL.Read)
  })

  it('falls back to the paired scheduled row as the last resort', () => {
    const scheduled = parsedOf(toolEvent(ZCODE_TOOL_KIND.Scheduled, { toolName: ZCODE_TOOL.Write }))
    expect(zcodeRow(result, undefined, scheduled).toolName).toBe(ZCODE_TOOL.Write)
  })

  it('reports an empty name when no source has one', () => {
    expect(zcodeRow(result, undefined, undefined).toolName).toBe('')
    expect(zcodeRow(result, '', undefined).toolName).toBe('')
    expect(zcodeRow(null, undefined, undefined).toolName).toBe('')
  })
})

describe('zcodeToolInput', () => {
  // A result row never carries the input, and the input is what a title needs -- the
  // command that ran, the file that was read.
  it('reaches the paired scheduled row when the row itself carries no input', () => {
    const scheduled = parsedOf(toolEvent(ZCODE_TOOL_KIND.Scheduled, {
      toolName: ZCODE_TOOL.Read,
      input: { file_path: '/tmp/a.ts' },
    }))
    expect(zcodeToolInput(zcodeRow(toolEvent(ZCODE_TOOL_KIND.Result, { result: {} }), undefined, scheduled)))
      .toEqual({ file_path: '/tmp/a.ts' })
  })

  it('prefers its own non-empty input over the sibling', () => {
    const scheduled = parsedOf(toolEvent(ZCODE_TOOL_KIND.Scheduled, { input: { from: 'sibling' } }))
    expect(zcodeToolInput(zcodeRow(toolEvent(ZCODE_TOOL_KIND.Scheduled, { input: { from: 'own' } }), undefined, scheduled)))
      .toEqual({ from: 'own' })
  })

  // An `inputOmitted` scheduled row states `{}`, and the sibling is then the only
  // copy -- so an EMPTY own input must not win over a populated sibling.
  it('reaches the sibling when its own input is an empty object', () => {
    const scheduled = parsedOf(toolEvent(ZCODE_TOOL_KIND.Scheduled, { input: { from: 'sibling' } }))
    expect(zcodeToolInput(zcodeRow(toolEvent(ZCODE_TOOL_KIND.Result, { input: {} }), undefined, scheduled)))
      .toEqual({ from: 'sibling' })
  })

  it('reports an empty object when neither side has an input', () => {
    expect(zcodeToolInput(zcodeRow(toolEvent(ZCODE_TOOL_KIND.Result), undefined, undefined))).toEqual({})
    expect(zcodeToolInput(zcodeRow(null, undefined, undefined))).toEqual({})
  })
})

describe('zcodeErrorText', () => {
  const update = (payload: Record<string, unknown>) => zcodeExtractTool(toolEvent(ZCODE_TOOL_KIND.Error, payload))!

  it('prefers the error object message', () => {
    expect(zcodeErrorText(update({ error: { message: 'the reason' }, result: { content: 'ignored' } })))
      .toBe('the reason')
  })

  it('falls back to the result content when the error object has no message', () => {
    expect(zcodeErrorText(update({ error: { code: 'EACCES' }, result: { content: 'the body' } })))
      .toBe('the body')
  })

  it('reports an empty string when neither states anything', () => {
    expect(zcodeErrorText(update({}))).toBe('')
    expect(zcodeErrorText(update({ error: {} }))).toBe('')
  })
})

describe('zcodeTodoListFromInput', () => {
  it('reads the todos into the shared checklist shape', () => {
    const source = zcodeTodoListFromInput({
      todos: [
        { content: 'A', status: 'in_progress', activeForm: 'Doing A' },
        { content: 'B', status: 'completed' },
      ],
    })
    expect(source).toMatchObject({ toolName: 'TodoWrite', title: '2 tasks' })
    expect(source!.todos.map(t => [t.content, t.status])).toEqual([['A', 'in_progress'], ['B', 'completed']])
  })

  it('titles a single task in the singular', () => {
    expect(zcodeTodoListFromInput({ todos: [{ content: 'A', status: 'pending' }] })!.title).toBe('1 task')
  })

  // An emptied list is a real snapshot -- the renderer draws the cleared state for it.
  it('keeps an empty list', () => {
    expect(zcodeTodoListFromInput({ todos: [] })).toMatchObject({ title: '0 tasks', todos: [] })
  })

  it('refuses an input that carries no todos array', () => {
    expect(zcodeTodoListFromInput({ command: 'ls' })).toBeNull()
    expect(zcodeTodoListFromInput({ todos: 'nope' })).toBeNull()
    expect(zcodeTodoListFromInput({})).toBeNull()
    expect(zcodeTodoListFromInput(null)).toBeNull()
    expect(zcodeTodoListFromInput(undefined)).toBeNull()
  })
})

// The row is what the six extractors take, and its point is that the three sources
// travel together. A result row carries no tool name of its own, so the sibling is the
// only source for it -- and when the arguments were positional and optional, a call
// site that omitted the sibling compiled and rendered a nameless row.
describe('zcodeRow', () => {
  const resultRow = toolEvent(ZCODE_TOOL_KIND.Result, { result: { content: 'ok' } })
  const scheduledSibling = parsedOf(
    toolEvent(ZCODE_TOOL_KIND.Scheduled, { toolName: ZCODE_TOOL.Bash, input: { command: 'ls' } }),
  )

  it('resolves the tool name once, in the payload > span > sibling order', () => {
    const own = toolEvent(ZCODE_TOOL_KIND.Scheduled, { toolName: ZCODE_TOOL.Read })
    expect(zcodeRow(own, ZCODE_TOOL.Bash, scheduledSibling).toolName).toBe(ZCODE_TOOL.Read)
    expect(zcodeRow(resultRow, ZCODE_TOOL.Write, scheduledSibling).toolName).toBe(ZCODE_TOOL.Write)
    expect(zcodeRow(resultRow, undefined, scheduledSibling).toolName).toBe(ZCODE_TOOL.Bash)
    expect(zcodeRow(resultRow, undefined, undefined).toolName).toBe('')
  })

  it('carries the sibling through, which is what a result row needs for its input', () => {
    expect(zcodeToolInput(zcodeRow(resultRow, undefined, scheduledSibling))).toEqual({ command: 'ls' })
    // Without the sibling there is nothing to fall back to. This is the state a
    // forgotten argument used to produce silently; it is now a type error at the call
    // site, and only an explicit `undefined` can reach it.
    expect(zcodeToolInput(zcodeRow(resultRow, undefined, undefined))).toEqual({})
  })

  it('prefers the input the row itself carries over the sibling', () => {
    const own = toolEvent(ZCODE_TOOL_KIND.Scheduled, { toolName: ZCODE_TOOL.Bash, input: { command: 'pwd' } })
    expect(zcodeToolInput(zcodeRow(own, undefined, scheduledSibling))).toEqual({ command: 'pwd' })
  })
})
