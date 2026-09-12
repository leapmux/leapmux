import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { testMessageSources } from '~/test-support/messageRenderSources'
import { renderMessageContent } from '../../messageRenderers'
import { toolUseHeader } from '../../toolStyles.css'
import { renderACPToolPair } from '../acp/testUtils'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import './plugin'
import '../testMocks'

function renderTool(fields: Record<string, unknown>, extra?: Record<string, unknown>) {
  const tool = { sessionUpdate: 'tool_call_update', status: 'completed', toolCallId: 'copilot-tool', ...fields }
  const category = providerFor(AgentProvider.GITHUB_COPILOT)!.classify(input(tool))
  const parsed = { ...input(tool), supplementalContent: extra ? { sessionUpdate: tool.sessionUpdate, status: tool.status, toolCallId: tool.toolCallId, ...extra } : undefined }
  return render(() => renderMessageContent(tool, { premeasureMode: true, sources: testMessageSources({ current: () => parsed }) }, category, AgentProvider.GITHUB_COPILOT))
}

describe('copilot native tool rendering', () => {
  it('shows one failure status for a native agent result', () => {
    const { container } = renderTool({ kind: 'other', status: 'failed', rawOutput: { message: 'The child failed.' } }, {
      nativeEvents: [{ type: 'tool.execution_start', data: { toolCallId: 'copilot-tool', toolName: 'task', arguments: { description: 'Inspect project' } } }],
    })
    expect(container.textContent).toContain('Agent "Inspect project" failed')
    expect(container.querySelectorAll('svg.lucide-circle-alert')).toHaveLength(1)
  })
  it('does not render a recovered result in the request row', () => {
    const { container } = renderTool({ sessionUpdate: 'tool_call', status: 'pending', kind: 'other', title: 'Task' }, {
      nativeEvents: [
        { type: 'tool.execution_start', data: { toolCallId: 'copilot-tool', toolName: 'task', arguments: { description: 'Inspect project', prompt: 'Read the entry points.' } } },
        { type: 'tool.execution_complete', data: { toolCallId: 'copilot-tool', result: { content: 'Recovered report belongs to the result' } } },
      ],
    })
    expect(container.textContent).toContain('Read the entry points.')
    expect(container.textContent).not.toContain('Recovered report belongs to the result')
  })
  it('uses the native task identity and request for the shared agent result', () => {
    const args = { description: 'Inspect the project', prompt: 'Read the entry points.', agent_type: 'explore', mode: 'sync' }
    const { container } = renderACPToolPair(AgentProvider.GITHUB_COPILOT, {
      kind: 'other',
      title: 'Inspect the project',
      rawInput: args,
    }, { rawOutput: { content: '**Findings**\n\nThe entry points exist.' } }, {
      nativeEvents: [
        { type: 'tool.execution_start', data: { toolCallId: 'call', toolName: 'task', arguments: args } },
        { type: 'subagent.started', agentId: 'child', data: { toolCallId: 'call' } },
        { type: 'subagent.completed', agentId: 'child', data: { toolCallId: 'call', totalToolCalls: 2, durationMs: 1000 } },
      ],
    })
    expect(container.textContent).toContain('Inspect the project (explore)')
    expect(container.textContent).toContain('Agent "Inspect the project" completed')
    expect(container.querySelector('strong')?.textContent).toBe('Findings')
    expect(container.textContent).not.toContain('Arguments')
  })
  it('keeps a matching file whose name starts with the no-match phrase', () => {
    const { container } = renderTool({ kind: 'other', title: 'Searching for needle', rawInput: { pattern: 'needle', paths: ['No matches found.ts'] }, rawOutput: { content: 'No matches found.ts:needle' } })
    expect(container.textContent).toContain('No matches found.ts:needle')
  })

  it('counts matches when the requested context is zero', () => {
    const { container } = renderTool({ kind: 'other', title: 'Searching for needle', rawInput: { pattern: 'needle', paths: ['/project'], C: 0 }, rawOutput: { content: 'a.ts:needle\na.ts:needle again' } })
    expect(container.textContent).toContain('Found 2 matches')
  })

  it('does not count context lines when the native flag has a leading dash', () => {
    const { container } = renderTool({ kind: 'other', title: 'Searching for needle', rawInput: { 'pattern': 'needle', 'paths': ['/project'], '-C': 1 }, rawOutput: { content: 'a.ts:before\na.ts:needle\na.ts:after' } })
    expect(container.textContent).not.toContain('Found 3 matches')
    expect(container.textContent).toContain('a.ts:needle')
  })

  it('shows a single native path-array target for a glob request', () => {
    const { container } = renderTool({ sessionUpdate: 'tool_call', status: 'pending', kind: 'read', title: 'Finding files matching *.ts', rawInput: { pattern: '*.ts', paths: ['/project'] } })
    expect(container.textContent).toContain('/project')
  })
  it('shows every requested search path from the native array', () => {
    const { container } = renderTool({
      sessionUpdate: 'tool_call',
      status: 'pending',
      title: 'Searching for answer',
      kind: 'other',
      rawInput: { pattern: 'answer', paths: ['/project/one', '/project/two'], output_mode: 'content' },
    })
    expect(container.textContent).toContain('/project/one')
    expect(container.textContent).toContain('/project/two')
  })

  it('renders a pending file creation from the text patch request', () => {
    const { container } = renderTool({ sessionUpdate: 'tool_call', status: 'pending', kind: 'edit', title: 'apply_patch', rawInput: '*** Begin Patch\n*** Add File: created.txt\n+First line\n+Second line\n*** End Patch\n' })
    expect(container.querySelector(`.${toolUseHeader}`)?.textContent).toBe('created.txt (2 lines)')
    expect(container.querySelector('[data-file-diff]')).toBeNull()
  })

  it('shows every requested file in a patch with multiple operations', () => {
    const { container } = renderTool({ sessionUpdate: 'tool_call', status: 'pending', kind: 'edit', title: 'apply_patch', rawInput: '*** Begin Patch\n*** Update File: sample.ts\n@@\n-before\n+after\n*** Delete File: obsolete.ts\n*** End Patch' })
    expect(container.querySelector(`.${toolUseHeader}`)?.textContent).toBe('2 files')
    expect(container.textContent).toContain('sample.ts')
    expect(container.textContent).toContain('obsolete.ts')
    expect(container.textContent).toContain('+1')
    expect(container.textContent).toContain('-1')
    expect(container.querySelector('[data-file-diff]')).toBeNull()
  })

  it('shows both paths for a requested move', () => {
    const { container } = renderTool({ sessionUpdate: 'tool_call', status: 'pending', kind: 'edit', title: 'apply_patch', rawInput: '*** Begin Patch\n*** Update File: old name.ts\n*** Move to: new name.ts\n*** End Patch' })
    expect(container.querySelector(`.${toolUseHeader}`)?.textContent).toBe('old name.ts → new name.ts')
    expect(container.querySelector('[data-file-diff]')).toBeNull()
  })

  it('retains an incomplete patch as readable request text', () => {
    const patch = '*** Begin Patch\n*** Add File: incomplete.ts\n+unfinished'
    const { container } = renderTool({ sessionUpdate: 'tool_call', status: 'pending', kind: 'edit', title: 'apply_patch', rawInput: patch })
    expect(container.textContent).toContain(patch)
    expect(container.querySelector('[data-file-diff]')).toBeNull()
  })

  it('renders native file search as a search rather than a file read', () => {
    const { container } = renderTool({
      title: 'Finding files matching *.py',
      kind: 'read',
      rawInput: { pattern: '*.py', paths: '/project' },
      rawOutput: { content: '/project/first.py\n/project/second.py' },
    })
    expect(container.textContent).toContain('Found 2 files')
    expect(container.textContent).toContain('*.py')
    expect(container.textContent).toContain('second.py')
  })

  it('renders native grep output without inventing missing line numbers', () => {
    const { container } = renderTool({
      title: 'Searching for \'answer\'',
      kind: 'other',
      rawInput: { pattern: 'answer', paths: '/project', output_mode: 'content' },
      rawOutput: { content: '/project/a.py:answer = 42\n/project/a.py:print(answer)' },
    })
    expect(container.textContent).toContain('Found 2 matches')
    expect(container.textContent).toContain('/project/a.py:answer = 42')
    expect(container.textContent).not.toContain('/project/a.py:1:')
  })

  it('renders file content that the view tool sends only in rawOutput', () => {
    const { container } = renderTool({
      kind: 'read',
      rawInput: { path: '/project/sample.py', view_range: [5, 6] },
      rawOutput: { content: 'answer = 42\nprint(answer)' },
    })
    expect(container.textContent).toContain('answer = 42')
    expect(container.textContent).toContain('5')
    expect(container.textContent).toContain('6')
  })

  it('extracts the shell status trailer without showing it as output', () => {
    const { container } = renderTool({
      kind: 'execute',
      rawInput: { command: 'python3 sample.py' },
      content: [{ type: 'content', content: { type: 'text', text: 'invalid argument\n<shellId: 0 completed with exit code 2>' } }],
    })
    expect(container.textContent).toContain('invalid argument')
    expect(container.textContent).toContain('Error (exit 2)')
    expect(container.textContent).not.toContain('<shellId:')
  })

  it('preserves raw error messages without a content block', () => {
    const { container } = renderTool({
      kind: 'read',
      status: 'failed',
      rawInput: { path: '/missing.py' },
      rawOutput: { message: 'File does not exist', code: 'ENOENT' },
    })
    expect(container.textContent).toContain('File does not exist')
    expect(container.textContent).toContain('Failed')
  })
})
