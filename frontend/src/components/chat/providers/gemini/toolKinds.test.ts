import { describe, expect, it } from 'vitest'
import { GEMINI_TOOL } from '~/generated/contracts/gemini-protocol'
import { acpToolCall } from '../acp/extractors/toolCall'
import { geminiToolCallAdapter } from './extractors/toolCall'
import { GEMINI_TOOL_KINDS, isGeminiTool } from './toolKinds'

describe('GEMINI_TOOL_KINDS', () => {
  // The whole table, so a name that joins it or leaves it fails here first.
  it('maps each tool that the shared build reads at a kind of its own', () => {
    expect(GEMINI_TOOL_KINDS).toStrictEqual({
      [GEMINI_TOOL.RunShellCommand]: 'execute',
      [GEMINI_TOOL.ReadFile]: 'read',
      [GEMINI_TOOL.WriteFile]: 'write',
      [GEMINI_TOOL.Replace]: 'edit',
    })
  })

  // A live frame states the tool only in the call identifier, before `__`. The wire
  // kind here is `other`, so the kind of the call comes from the table alone. The file
  // comes from `locations`, as it does in the frames that Gemini CLI sends.
  it.each(Object.entries(GEMINI_TOOL_KINDS))('builds a running %s call at the %s kind', (name, kind) => {
    const call = acpToolCall({
      sessionUpdate: 'tool_call',
      toolCallId: `${name}__live`,
      status: 'pending',
      kind: 'other',
      title: name,
      content: [],
      locations: [{ path: '/w/notes.txt' }],
    }, geminiToolCallAdapter, undefined)
    expect(call.kind).toBe(kind)
    expect(call.name).toBe(name)
    expect(call.degradation).toBeUndefined()
  })
})

describe('isGeminiTool', () => {
  it.each(Object.keys(GEMINI_TOOL_KINDS))('accepts %j', (name) => {
    expect(isGeminiTool(name)).toBe(true)
  })

  // The names that the adapter builds from branches of its own, the names that the
  // shared build reads at the wire kind, and the names that every object inherits.
  it.each([
    GEMINI_TOOL.TodoWrite,
    GEMINI_TOOL.InvokeAgent,
    GEMINI_TOOL.EnterPlanMode,
    GEMINI_TOOL.ExitPlanMode,
    GEMINI_TOOL.CompleteTask,
    'toString',
    'constructor',
    '__proto__',
    '',
  ])('refuses %j', (name) => {
    expect(isGeminiTool(name)).toBe(false)
  })
})
