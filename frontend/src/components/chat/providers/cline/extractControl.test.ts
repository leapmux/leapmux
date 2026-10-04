import { describe, expect, it } from 'vitest'
import { clineExtractControl } from './extractControl'
import { CLINE_SPAWN_WARNING } from './spawnWarning'

/** A stored approval request, with the arguments as the raw `inputJson` text. */
function approval(toolName: unknown, inputJson: unknown): Record<string, unknown> {
  return {
    version: 'v1',
    event: 'approval.requested',
    sessionId: 's1',
    payload: { approvalId: 'approval_1', toolCallId: 'call_1', toolName, inputJson },
  }
}

describe('clineExtractControl', () => {
  // `plugin.test.ts` covers the approvals that the worker publishes for each tool. The
  // cases here are the arguments and the names that Cline can state in other shapes.
  it('reads JSON arguments that are not an object as none', () => {
    for (const inputJson of ['[1,2]', '"text"', 'null', '3', undefined]) {
      const control = clineExtractControl({ payload: approval('editor', inputJson) })
      expect(control?.kind === 'permission' ? control.permission.input : 'no permission', String(inputJson)).toEqual({})
    }
  })

  it('titles an approval that states no tool as a tool', () => {
    expect(clineExtractControl({ payload: approval('', '{}') })).toEqual({ kind: 'permission', permission: { title: 'Tool', input: {}, options: [] } })
    expect(clineExtractControl({ payload: approval(7, '{}') })).toEqual({ kind: 'permission', permission: { title: 'Tool', input: {}, options: [] } })
  })

  it('reads the one command of a call that states `command`', () => {
    const control = clineExtractControl({ payload: approval('run_commands', '{"command":"make test"}') })
    expect(control?.kind === 'permission' ? control.permission.command : undefined).toBe('make test')
  })

  // An empty command line is no command, so the banner draws the arguments alone.
  it('states no command for a command call that states none', () => {
    for (const inputJson of ['{}', '{"commands":[]}', '{"commands":["",{"args":[]}]}']) {
      const control = clineExtractControl({ payload: approval('run_commands', inputJson) })
      expect(control?.kind === 'permission' ? 'command' in control.permission : 'no permission', inputJson).toBe(false)
    }
  })

  // Only a shell call has a command line. A `command` field of another tool is one
  // of its arguments.
  it('reads a `command` argument of a tool that is not a shell as an argument alone', () => {
    const control = clineExtractControl({ payload: approval('github__deploy', '{"command":"ship"}') })
    expect(control).toEqual({ kind: 'permission', permission: { title: 'github__deploy', input: { command: 'ship' }, options: [] } })
  })

  it('warns for the configured agent prefix alone, and not for a name that only holds it', () => {
    const reason = (tool: string) => {
      const control = clineExtractControl({ payload: approval(tool, '{}') })
      return control?.kind === 'permission' ? control.permission.reason : 'no permission'
    }
    expect(reason('subagent_')).toBe(CLINE_SPAWN_WARNING)
    expect(reason('mcp__subagent_reviewer')).toBeUndefined()
  })

  it('reads the plan approval whatever its arguments state', () => {
    expect(clineExtractControl({ payload: approval('switch_to_act_mode', 'not json') })).toEqual({ kind: 'plan' })
  })

  it('answers null for an envelope of another event, and for a row that is not an envelope', () => {
    expect(clineExtractControl({ payload: { version: 'v1', event: 'tool.started', payload: { toolName: 'editor' } } })).toBeNull()
    expect(clineExtractControl({ payload: { payload: { toolName: 'editor', inputJson: '{}' } } })).toBeNull()
  })
})
