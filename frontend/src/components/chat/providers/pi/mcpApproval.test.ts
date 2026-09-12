import { describe, expect, it } from 'vitest'
import { input } from '../testUtils'
import { isPiMcpApproval, piMcpApproval } from './mcpApproval'

function dialog(preview: string) {
  return { type: 'extension_ui_request', method: 'select', title: `MCP: probe wants to run write\n\nArguments:\n${preview}`, options: ['Allow once', 'Allow for session', 'Deny'] }
}

describe('pi MCP permission source', () => {
  it('recovers JSON-string arguments accepted by the MCP gateway', () => {
    const args = { text: 'x'.repeat(600), end: 'complete' }
    const preview = `${JSON.stringify(args, null, 2).replace(/\s+/g, ' ').slice(0, 500)}...`
    const source = input({ type: 'tool_execution_start', toolName: 'mcp', args: { tool: 'probe_write', args: JSON.stringify(args) } })
    expect(piMcpApproval(dialog(preview), source)?.arguments).toEqual(args)
  })

  it('restores whitespace and full arguments from the matching tool request', () => {
    const args = { text: `multiple   spaces${'x'.repeat(600)}`, end: 'complete' }
    const preview = `${JSON.stringify(args, null, 2).replace(/\s+/g, ' ').slice(0, 500)}...`
    const source = input({ type: 'tool_execution_start', toolName: 'mcp', args: { tool: 'probe_write', args } })
    expect(piMcpApproval(dialog(preview), source)?.arguments).toEqual(args)
    expect(piMcpApproval(dialog(preview), source)?.argumentNotice).toBeUndefined()
  })

  it('keeps the native preview when the source is missing or does not match', () => {
    const request = dialog('{ "text": "partial...')
    const source = input({ type: 'tool_execution_start', toolName: 'mcp', args: { tool: 'probe_write', args: { different: true } } })
    expect(piMcpApproval(request, source)?.arguments).toBe('{ "text": "partial...')
    expect(piMcpApproval(request)?.argumentNotice).toContain('truncated')
  })

  it('does not classify a general question or a changed option list as a permission request', () => {
    expect(isPiMcpApproval({ ...dialog('{}'), title: 'Choose a value' })).toBe(false)
    expect(isPiMcpApproval({ ...dialog('{}'), options: ['Allow once', 'Deny'] })).toBe(false)
    expect(isPiMcpApproval({ ...dialog('{}'), method: 'input' })).toBe(false)
  })
})
