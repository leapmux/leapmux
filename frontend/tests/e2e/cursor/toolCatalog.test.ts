import { describe, expect, it } from 'vitest'
import { cursorToolCasesFromBundle } from './toolCatalog'

function bundle(descriptor: string) {
  return `class NativeToolCall { static $(){return[${JSON.stringify(descriptor)}]}}`
}

describe('cursorToolCasesFromBundle', () => {
  it('extracts the exact tool union while ignoring unrelated generated classes', () => {
    expect(cursorToolCasesFromBundle(bundle('ToolCall|1 shell_tool_call #0 tool|19 task_tool_call #14 tool|57 tool_call_id 9') + bundle('Other|1 input 9')))
      .toEqual(['shell_tool_call', 'task_tool_call'])
  })

  it('retains a new executor branch so the native negative test cannot silently ignore it', () => {
    expect(cursorToolCasesFromBundle(bundle('ToolCall|1 code_execution_tool_call #0 tool|19 task_tool_call #14 tool')))
      .toContain('code_execution_tool_call')
  })

  it.each([
    ['', 'The installed Cursor bundle has no unique native ToolCall schema.'],
    [bundle('Other|1 input 9'), 'The installed Cursor bundle has no unique native ToolCall schema.'],
    [bundle('ToolCall|19 task_tool_call #14 tool|19 task_tool_call #14 tool'), 'The installed Cursor tool schema is empty or repeats a case.'],
  ])('refuses missing or repeated tool definitions: %s', (source, error) => {
    expect(() => cursorToolCasesFromBundle(source)).toThrow(error)
  })
})
