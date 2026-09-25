import { describe, expect, it } from 'vitest'
import { ampExtractControl } from './extractControl'

function request(tool: string, input: unknown, extra: Record<string, unknown> = {}): Record<string, unknown> {
  return { type: 'leapmux_amp_permission', tool_name: tool, tool_use_id: 'TU-034UC14fL0WVIuQhmDl0qN', input, ...extra }
}

describe('ampExtractControl', () => {
  it('reads a shell call with its command and its directory', () => {
    expect(ampExtractControl({ payload: request('shell_command', { command: 'rm -rf build', workdir: '/work' }) })).toEqual({
      kind: 'permission',
      permission: {
        title: 'shell_command',
        input: { command: 'rm -rf build', workdir: '/work' },
        command: 'rm -rf build',
        workingDirectory: '/work',
        options: [],
      },
    })
  })

  // The legacy `Bash` tool of an old thread states its command as `cmd`, and the
  // banner reads it as the transcript does.
  it('reads the command of a legacy Bash call', () => {
    const surface = ampExtractControl({ payload: request('Bash', { cmd: 'make clean' }) })
    expect(surface?.kind === 'permission' ? surface.permission.command : 'none').toBe('make clean')
  })

  // The executor runs `shell_command` as `async_shell_command`, so a helper run can
  // state either name.
  it.each(['shell_command', 'async_shell_command', 'Bash'])('reads the command and the directory of a %s call', (tool) => {
    const surface = ampExtractControl({ payload: request(tool, { command: 'npm test', workdir: '/work' }) })
    expect(surface?.kind === 'permission' ? [surface.permission.command, surface.permission.workingDirectory] : 'none').toEqual(['npm test', '/work'])
  })

  it('states no command for a shell call whose command is blank', () => {
    expect(ampExtractControl({ payload: request('shell_command', { command: '', workdir: '/work' }) })).toEqual({
      kind: 'permission',
      permission: { title: 'shell_command', input: { command: '', workdir: '/work' }, workingDirectory: '/work', options: [] },
    })
  })

  it('reads another tool with its arguments and no command', () => {
    const input = { patchText: '*** Begin Patch\n*** End Patch' }
    expect(ampExtractControl({ payload: request('apply_patch', input) })).toEqual({
      kind: 'permission',
      permission: { title: 'apply_patch', input, options: [] },
    })
  })

  it('reads a `command` argument of a tool that is not a shell as an argument alone', () => {
    const surface = ampExtractControl({ payload: request('mcp__ci__run', { command: 'deploy' }) })
    expect(surface?.kind === 'permission' ? surface.permission.command : 'none').toBeUndefined()
  })

  it('reads a request with no input and no tool', () => {
    expect(ampExtractControl({ payload: { type: 'leapmux_amp_permission' } })).toEqual({
      kind: 'permission',
      permission: { title: 'Tool', input: {}, options: [] },
    })
    expect(ampExtractControl({ payload: request('shell_command', 'not an object') })).toEqual({
      kind: 'permission',
      permission: { title: 'shell_command', input: {}, options: [] },
    })
  })

  it('answers null for a payload that is not the envelope', () => {
    expect(ampExtractControl({ payload: {} })).toBeNull()
    expect(ampExtractControl({ payload: { request: { tool_name: 'Bash', input: {} } } })).toBeNull()
  })
})
