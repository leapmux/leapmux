import type { ToolPresentation } from '~/components/chat/results/toolPresentation'
import { describe, expect, it } from 'vitest'
import { REASONIX_CAPABILITY_ACTION, REASONIX_TOOL } from '~/generated/contracts/reasonix-protocol'
import { acpToolPresentation } from '../acp/toolPresentation'
import { reasonixToolAdapter } from './toolPresentation'

function model(tool: Record<string, unknown>): ToolPresentation {
  return acpToolPresentation({ sessionUpdate: 'tool_call_update', toolCallId: 'reasonix-tool', status: 'completed', kind: 'other', ...tool }, reasonixToolAdapter)
}

function capability(capabilityId: string): ToolPresentation {
  return model({
    title: REASONIX_TOOL.UseCapability,
    rawInput: { action: REASONIX_CAPABILITY_ACTION.Call, capability_id: capabilityId, arguments: { query: 'needle' } },
  })
}

describe('reasonixToolAdapter Model Context Protocol names', () => {
  it('splits a native tool name into its server and its tool', () => {
    const presentation = model({ title: 'mcp__server__lookup' })
    expect(presentation.label).toBe('MCP Tool Call')
    expect(presentation.title).toBe('server / lookup')
  })

  it('keeps every further separator in the tool half', () => {
    expect(model({ title: 'mcp__server__group__lookup' }).title).toBe('server / group__lookup')
  })

  // An empty half labels the row with nothing, which states less than the raw name.
  it.each(['mcp__server__', 'mcp____lookup', 'mcp__server', 'mcp__'])('refuses the name %s', (name) => {
    const presentation = model({ title: name })
    expect(presentation.label).not.toBe('MCP Tool Call')
    expect(presentation.title).toBe(name)
  })

  it('splits a capability identifier into its server and its tool', () => {
    const presentation = capability('mcp-tool:server/lookup')
    expect(presentation.label).toBe('MCP Tool Call')
    expect(presentation.title).toBe('server / lookup')
    expect(presentation.input).toEqual({ query: 'needle' })
  })

  it.each(['mcp-tool:server/', 'mcp-tool:/lookup', 'mcp-tool:lookup', 'mcp-tool:'])('refuses the capability identifier %s', (id) => {
    expect(capability(id).label).not.toBe('MCP Tool Call')
  })
})

describe('reasonixToolAdapter subagent launches', () => {
  it('titles the row from the description and draws the report', () => {
    const presentation = model({ title: REASONIX_TOOL.Task, rawInput: { description: 'Inspect sample', profile: 'explore', prompt: 'Read it' }, content: [{ type: 'content', content: { type: 'text', text: 'Found two' } }] })
    expect(presentation.kind).toBe('agent')
    expect(presentation.title).toBe('Inspect sample')
    expect(presentation.agentRequest).toEqual({ toolName: 'Task', description: 'Inspect sample', agentType: 'explore', prompt: 'Read it' })
    expect(presentation.body.type).toBe('agent')
  })

  it('falls back to the shared word when the launch describes nothing', () => {
    expect(model({ title: REASONIX_TOOL.ReadOnlyTask, rawInput: { prompt: 'Read it' }, status: 'pending' }).title).toBe('Task')
  })
})

describe('reasonixToolAdapter stored tool records', () => {
  // The supplement echoes the call it answers, which is what the shared ACP resolver
  // matches it on before the plugin sees it.
  function withRecord(record: Record<string, unknown>, tool: Record<string, unknown> = {}): ToolPresentation {
    const call = { sessionUpdate: 'tool_call_update', toolCallId: 'reasonix-tool', status: 'completed', kind: 'other', content: [{ type: 'content', content: { type: 'text', text: 'clipped…(4 more chars truncated)' } }], ...tool }
    const supplemental = { sessionUpdate: call.sessionUpdate, toolCallId: call.toolCallId, status: call.status, rawOutput: { reasonix: { role: 'tool', tool_call_id: 'reasonix-tool', ...record } } }
    return acpToolPresentation(call, reasonixToolAdapter, supplemental)
  }

  // The plugin reads `raw_content` first and `content` second, so a field name that
  // stopped matching falls through to the shorter body instead of failing the build.
  // The Go tags are pinned to the same contract in reasonix_tool_store_test.go.
  it('prefers the unabridged body the record carries', () => {
    expect(withRecord({ name: 'bash', content: 'short body', raw_content: 'the whole body' }).output).toBe('the whole body')
    expect(withRecord({ name: 'bash', content: 'short body' }).output).toBe('short body')
  })

  it('takes the tool name from the record when the call states none', () => {
    expect(withRecord({ name: 'ls' }, { title: '' }).body.type).toBe('directory')
    expect(withRecord({}, { title: '' }).body.type).not.toBe('directory')
  })

  it('refuses a record that answers another call', () => {
    expect(withRecord({ tool_call_id: 'another-call', name: 'bash', content: 'stored body' }).output).toContain('clipped')
  })

  it('refuses a record that is not a tool record', () => {
    expect(withRecord({ role: 'assistant', name: 'bash', content: 'stored body' }).output).toContain('clipped')
  })
})
