import { describe, expect, it } from 'vitest'
import { codebuddyNotificationEntry } from './notification'

describe('codebuddyNotificationEntry', () => {
  it('states one failed server and one late server in the singular', () => {
    expect(codebuddyNotificationEntry({ type: 'system', subtype: 'mcp_status', event: 'finish', failed: ['probe'], timed_out: ['slow'] })).toEqual([
      { kind: 'text', text: 'Failed to start MCP server: probe' },
      { kind: 'text', text: 'MCP server did not start in time: slow' },
    ])
  })

  it('states several late servers in the plural', () => {
    expect(codebuddyNotificationEntry({ type: 'system', subtype: 'mcp_status', event: 'finish', failed: [], timed_out: ['a', 'b'] })).toEqual([
      { kind: 'text', text: 'MCP servers did not start in time: a, b' },
    ])
  })

  it('skips a list entry that is no server name, and a list that is absent', () => {
    expect(codebuddyNotificationEntry({ type: 'system', subtype: 'mcp_status', event: 'finish', failed: [7, '', ' ', 'probe', null] })).toEqual([
      { kind: 'text', text: 'Failed to start MCP server: probe' },
    ])
    expect(codebuddyNotificationEntry({ type: 'system', subtype: 'mcp_status', event: 'finish', failed: 'probe' })).toEqual([])
  })

  it('holds no entry for the progress events of the MCP servers', () => {
    expect(codebuddyNotificationEntry({ type: 'system', subtype: 'mcp_status', event: 'start', servers: ['probe'], failed: ['probe'] })).toEqual([])
    expect(codebuddyNotificationEntry({ type: 'system', subtype: 'mcp_status', event: 'server', name: 'probe', state: 'failed', error: 'x' })).toEqual([])
  })

  it('states an informational line, with "Warning" only for the warning level', () => {
    expect(codebuddyNotificationEntry({ type: 'system', subtype: 'informational', level: 'warning', content: ' Blocked. ' })).toEqual([{ kind: 'text', text: 'Warning: Blocked.' }])
    expect(codebuddyNotificationEntry({ type: 'system', subtype: 'informational', level: 'info', content: 'Resumed.' })).toEqual([{ kind: 'text', text: 'Resumed.' }])
    expect(codebuddyNotificationEntry({ type: 'system', subtype: 'informational', level: 'warning' })).toEqual([])
  })

  it('states the message of an error line, and an error with no message as an error', () => {
    expect(codebuddyNotificationEntry({ type: 'error', error: ' Request failed. ' })).toEqual([{ kind: 'text', text: 'Error: Request failed.' }])
    expect(codebuddyNotificationEntry({ type: 'error', error: 7 })).toEqual([{ kind: 'text', text: 'Error' }])
  })

  it('reads no line of another type or another subtype', () => {
    expect(codebuddyNotificationEntry({ type: 'assistant', subtype: 'informational', content: 'x' })).toEqual([])
    expect(codebuddyNotificationEntry({ type: 'system', subtype: 'init', content: 'x' })).toEqual([])
    expect(codebuddyNotificationEntry({ subtype: 'mcp_status', event: 'finish', failed: ['probe'] })).toEqual([])
  })
})
