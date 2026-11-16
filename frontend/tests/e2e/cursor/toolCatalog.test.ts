import { describe, expect, it } from 'vitest'
import { cursorExecutionCasesFromBundle } from './toolCatalog'

function bundle(descriptor: string) {
  return `class NativeToolCall { static $(){return[${JSON.stringify(descriptor)}]}}`
}

describe('cursorExecutionCasesFromBundle', () => {
  it('reads executable client requests independently of stored tool records', () => {
    const source = bundle('ToolCall|19 task_tool_call #0 tool|1000 execute_tool_call #1 tool')
      + bundle('ExecServerMessage|1 id 13|2 shell_args #0 message|27 execute_hook_args #1 message')
    expect(cursorExecutionCasesFromBundle(source)).toEqual(['shell_args', 'execute_hook_args'])
  })

  it('extracts the exact tool union while ignoring unrelated generated classes', () => {
    expect(cursorExecutionCasesFromBundle(bundle('ExecServerMessage|1 id 13|2 shell_args #0 message|28 subagent_args #1 message|19 span_context #2?') + bundle('Other|1 input 9')))
      .toEqual(['shell_args', 'subagent_args'])
  })

  it('retains a new executor branch so the native negative test cannot silently ignore it', () => {
    expect(cursorExecutionCasesFromBundle(bundle('ExecServerMessage|2 shell_args #0 message|1000 code_execution_args #1 message')))
      .toContain('code_execution_args')
  })

  it.each([
    ['', 'The installed Cursor bundle has no unique native ExecServerMessage schema.'],
    [
      bundle('Other|1 input 9'),
      'The installed Cursor bundle has no unique native ExecServerMessage schema.',
    ],
    [
      bundle('ExecServerMessage|2 shell_args #0 message|3 shell_args #1 message'),
      'The installed Cursor execution schema is empty or repeats a case.',
    ],
  ])('refuses missing or repeated tool definitions: %s', (source, error) => {
    expect(() => cursorExecutionCasesFromBundle(source)).toThrow(error)
  })
})
