import type { ToolPresentation } from '~/components/chat/results/toolPresentation'
import { describe, expect, it } from 'vitest'
import { acpToolPresentation } from '../acp/toolPresentation'
import { openCodeToolAdapter } from './toolPresentation'

function model(tool: Record<string, unknown>): ToolPresentation {
  return acpToolPresentation({ sessionUpdate: 'tool_call', toolCallId: 'open-code-tool', status: 'pending', kind: 'think', title: 'task', ...tool }, openCodeToolAdapter)
}

describe('openCodeToolAdapter subagent launches', () => {
  it('titles the row from the description', () => {
    const presentation = model({ rawInput: { description: 'Inspect project structure', subagent_type: 'explore', prompt: 'Read the entry points.' } })
    expect(presentation.kind).toBe('agent')
    expect(presentation.title).toBe('Inspect project structure')
    expect(presentation.agentRequest).toEqual({ toolName: 'Task', description: 'Inspect project structure', agentType: 'explore', prompt: 'Read the entry points.' })
  })

  it('falls back to the shared word when the launch describes nothing', () => {
    // This row read `Agent` before OpenCode joined the shared card, while Cursor and
    // Reasonix drew `Task` for the same launch.
    expect(model({ rawInput: { prompt: 'Read the entry points.' } }).title).toBe('Task')
  })

  it('keeps the tool label OpenCode gives the row', () => {
    expect(model({ rawInput: { description: 'Inspect project structure' } }).label).toBe('Task')
    expect(model({ rawInput: {} }).label).toBe('Task')
  })
})
