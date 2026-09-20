import type { SwitchModeRequest } from '../../../model/tools/switchMode'
import type { ClaudeToolRow } from './toolCommon'
import { describe, expect, it } from 'vitest'
import { CLAUDE_TOOL_NAMES } from '../toolNames'
import { claudeSwitchModeSpec } from './switchMode'

function row(toolName: string, fields: Partial<ClaudeToolRow> = {}): ClaudeToolRow {
  return {
    id: 'toolu_1',
    role: 'request',
    toolName,
    input: {},
    toolUseResult: undefined,
    resultContent: '',
    rawResultContent: undefined,
    images: [],
    isError: undefined,
    ...fields,
  }
}

/** The result row of a call the reader refused: an error flag over the feedback. */
function refused(toolName: string): ClaudeToolRow {
  return row(toolName, { role: 'result', isError: true, resultContent: 'Try a smaller scope.', toolUseResult: {} })
}

/** The result row of a call that proceeded. */
function approved(toolName: string, toolUseResult: Record<string, unknown> = {}): ClaudeToolRow {
  return row(toolName, { role: 'result', isError: false, resultContent: 'ok', toolUseResult })
}

describe('claudeSwitchModeSpec', () => {
  // The refusal of a plan is the ANSWER the agent asked for, and the shared header
  // knows no provider -- so the request carries the word this one tool states.
  it('words the refusal of a plan on the request', () => {
    const payload = claudeSwitchModeSpec({}, row(CLAUDE_TOOL_NAMES.EXIT_PLAN_MODE), refused(CLAUDE_TOOL_NAMES.EXIT_PLAN_MODE))
    expect(payload.statusOverride).toBe('declined')
    expect(payload.request.declinedTitle).toBe('Sent feedback')
    expect(payload.result).toEqual({ text: 'Try a smaller scope.', format: 'plain' })
  })

  it('keeps every other field the request already stated', () => {
    const request: SwitchModeRequest = { mode: 'plan', target: '/repo' }
    const payload = claudeSwitchModeSpec(request, row(CLAUDE_TOOL_NAMES.EXIT_PLAN_MODE), refused(CLAUDE_TOOL_NAMES.EXIT_PLAN_MODE))
    expect(payload.request).toStrictEqual({ mode: 'plan', target: '/repo', declinedTitle: 'Sent feedback' })
  })

  // One tool alone words its own refusal. Another switch that reported an error IS a
  // failure, so its request words nothing and the shared header states "Error".
  it('words no refusal for another mode switch', () => {
    const payload = claudeSwitchModeSpec({}, row(CLAUDE_TOOL_NAMES.ENTER_PLAN_MODE), refused(CLAUDE_TOOL_NAMES.ENTER_PLAN_MODE))
    expect(payload.request.declinedTitle).toBeUndefined()
    expect(payload.statusOverride).toBeUndefined()
  })

  it('words no refusal for a plan the reader approved', () => {
    const payload = claudeSwitchModeSpec({}, row(CLAUDE_TOOL_NAMES.EXIT_PLAN_MODE), approved(CLAUDE_TOOL_NAMES.EXIT_PLAN_MODE, { filePath: '/repo/.plans/one.md' }))
    expect(payload.request.declinedTitle).toBeUndefined()
    expect(payload.title).toBe('Plan approved')
    expect(payload.metadata).toEqual([{ label: 'Plan file', value: '/repo/.plans/one.md' }])
  })

  it('words no refusal while the call runs', () => {
    const payload = claudeSwitchModeSpec({}, row(CLAUDE_TOOL_NAMES.EXIT_PLAN_MODE), undefined)
    expect(payload.request.declinedTitle).toBeUndefined()
    expect(payload.result).toBeUndefined()
    expect(payload.title).toBe('Exit plan mode')
  })
})
