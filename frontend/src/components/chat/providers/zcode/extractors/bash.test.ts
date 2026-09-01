import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { ZCODE_EVENT, ZCODE_TOOL, ZCODE_TOOL_KIND } from '~/generated/contracts/zcode-protocol'
import { extractZCodeBash, zcodeBashToCommandSource } from './bash'
import { zcodeRow } from './toolCommon'

function toolEvent(kind: string, payload: Record<string, unknown>): Record<string, unknown> {
  return { type: ZCODE_EVENT.ToolUpdated, payload: { kind, toolCallId: 'call-1', ...payload } }
}

/** The scheduled row, which is the ONLY place the command itself lives. */
function scheduled(input: Record<string, unknown>, toolName: string = ZCODE_TOOL.Bash): ParsedMessageContent {
  const parent = toolEvent(ZCODE_TOOL_KIND.Scheduled, { toolName, input })
  return { rawText: '', topLevel: parent, parentObject: parent, wrapper: null }
}

function commandResult(result: Record<string, unknown>, extra: Record<string, unknown> = {}) {
  return toolEvent(ZCODE_TOOL_KIND.Result, { result, ...extra })
}

const perf = (command: Record<string, unknown>) => ({ detail: { kind: 'command', command } })

describe('extractZCodeBash', () => {
  it('reads the command from the scheduled row and the output from the result', () => {
    expect(extractZCodeBash(zcodeRow(commandResult({ content: 'total 48\nfile1\n', perf: perf({ exitCode: 0 }) }, { duration: 120 }), undefined, scheduled({ command: 'ls -la', description: 'list files' })))).toEqual({
      command: 'ls -la',
      description: 'list files',
      output: 'total 48\nfile1\n',
      exitCode: 0,
      timedOut: false,
      truncated: false,
      isError: false,
      durationMs: 120,
    })
  })

  // ZCode reports a ZERO exit code explicitly, unlike providers that surface only
  // failures -- so a null exit code genuinely means "unknown", never "succeeded".
  it('reports a null exit code when the app-server sent no command telemetry', () => {
    expect(extractZCodeBash(zcodeRow(commandResult({ content: 'out' }), ZCODE_TOOL.Bash, undefined))?.exitCode).toBeNull()
  })

  // The detail block is per-kind. Reading `command` off a patch detail would pick up
  // an unrelated shape, so the kind is checked before the block is trusted.
  it('ignores a perf detail whose kind is not command', () => {
    expect(extractZCodeBash(zcodeRow(commandResult({ content: 'out', perf: { detail: { kind: 'patch', command: { exitCode: 3 } } } }), ZCODE_TOOL.Bash, undefined))?.exitCode).toBeNull()
  })

  it('carries a non-zero exit code and the timedOut flag through', () => {
    const bash = extractZCodeBash(zcodeRow(commandResult({ content: 'boom', perf: perf({ exitCode: 137, timedOut: true }) }), ZCODE_TOOL.Bash, undefined))
    expect(bash).toMatchObject({ exitCode: 137, timedOut: true })
  })

  it('reports the error text of a failed call as the output', () => {
    expect(extractZCodeBash(zcodeRow(toolEvent(ZCODE_TOOL_KIND.Error, { error: { message: 'permission denied' } }), ZCODE_TOOL.Bash, undefined))).toMatchObject({ output: 'permission denied', isError: true })
  })

  it('falls back to the result content when a failed call carries no error message', () => {
    expect(extractZCodeBash(zcodeRow(toolEvent(ZCODE_TOOL_KIND.Error, { result: { content: 'the shell said no' } }), ZCODE_TOOL.Bash, undefined))).toMatchObject({ output: 'the shell said no', isError: true })
  })

  it('marks a result the app-server declared unsuccessful as an error', () => {
    expect(extractZCodeBash(zcodeRow(commandResult({ success: false, content: 'nope' }), ZCODE_TOOL.Bash, undefined))?.isError).toBe(true)
  })

  it('carries the truncated flag from the result', () => {
    expect(extractZCodeBash(zcodeRow(commandResult({ content: 'head', truncated: true }), ZCODE_TOOL.Bash, undefined))?.truncated).toBe(true)
  })

  it('reads the command off its own scheduled row, with no sibling', () => {
    expect(extractZCodeBash(zcodeRow(toolEvent(ZCODE_TOOL_KIND.Scheduled, { toolName: ZCODE_TOOL.Bash, input: { command: 'pwd' } }), undefined, undefined))).toMatchObject({ command: 'pwd', output: '' })
  })

  it('reports an empty command when neither the row nor the sibling has one', () => {
    expect(extractZCodeBash(zcodeRow(commandResult({ content: 'out' }), ZCODE_TOOL.Bash, undefined)))
      .toMatchObject({ command: '', description: '' })
  })

  it('returns null for another tool and for a row that is not a tool.updated', () => {
    expect(extractZCodeBash(zcodeRow(commandResult({ content: 'x' }), ZCODE_TOOL.Read, undefined))).toBeNull()
    expect(extractZCodeBash(zcodeRow(commandResult({ content: 'x' }), undefined, scheduled({}, ZCODE_TOOL.Read))))
      .toBeNull()
    expect(extractZCodeBash(zcodeRow({ type: ZCODE_EVENT.TurnCompleted, payload: {} }, ZCODE_TOOL.Bash, undefined))).toBeNull()
    expect(extractZCodeBash(zcodeRow(null, ZCODE_TOOL.Bash, undefined))).toBeNull()
  })

  // A tool.updated with no toolCallId identifies no call, so it cannot be paired with
  // a span and is not treated as one.
  it('returns null for a tool.updated carrying no toolCallId', () => {
    expect(extractZCodeBash(zcodeRow({ type: ZCODE_EVENT.ToolUpdated, payload: { kind: ZCODE_TOOL_KIND.Result, toolName: ZCODE_TOOL.Bash } }, ZCODE_TOOL.Bash, undefined))).toBeNull()
  })
})

describe('zcodeBashToCommandSource', () => {
  const base = {
    command: 'ls',
    description: '',
    output: 'out',
    exitCode: null as number | null,
    timedOut: false,
    truncated: false,
    isError: false,
    durationMs: null as number | null,
  }

  it('passes the output, duration and timeout through', () => {
    expect(zcodeBashToCommandSource({ ...base, durationMs: 42 })).toEqual({
      output: 'out',
      exitCode: undefined,
      durationMs: 42,
      interrupted: false,
      isError: false,
    })
  })

  // The app-server reports a FAILED command as a successful tool call whose content
  // says "Exit code 3", so the exit code is the only signal that the command failed.
  it('treats a non-zero exit as an error even when the call succeeded', () => {
    expect(zcodeBashToCommandSource({ ...base, exitCode: 3 }))
      .toMatchObject({ exitCode: 3, isError: true })
  })

  it('keeps a zero exit non-error', () => {
    expect(zcodeBashToCommandSource({ ...base, exitCode: 0 }))
      .toMatchObject({ exitCode: 0, isError: false })
  })

  it('treats a timeout as both interrupted and an error', () => {
    expect(zcodeBashToCommandSource({ ...base, timedOut: true }))
      .toMatchObject({ interrupted: true, isError: true })
  })

  it('keeps an error flag the extractor already set', () => {
    expect(zcodeBashToCommandSource({ ...base, isError: true }).isError).toBe(true)
  })
})
