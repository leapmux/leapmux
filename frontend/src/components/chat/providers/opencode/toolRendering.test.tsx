import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { todoList } from '~/components/todo/TodoList.css'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { renderMessageContent } from '../../messageRenderers'
import { renderACPToolPair } from '../acp/testUtils'
import { providerFor } from '../registry'
import { input, toolMessageInput } from '../testUtils'
import './index'
import '../kilo/plugin'
import '../testMocks'

describe.each([AgentProvider.OPENCODE, AgentProvider.KILO])('opencode protocol renderer (%s)', (provider) => {
  it('renders a native subagent report through the shared agent body', () => {
    const { container } = renderACPToolPair(provider, {
      title: 'task',
      kind: 'think',
      rawInput: { description: 'Inspect project structure', subagent_type: 'explore', prompt: 'Inspect the entry points.' },
    }, {
      rawOutput: { metadata: { sessionId: 'child-1', parentSessionId: 'parent-1' } },
      content: [{ type: 'content', content: { type: 'text', text: '<task id="child-1" state="completed">\n<task_result>\n**Findings**\n\n- Read the entry point.\n</task_result>\n</task>' } }],
    })
    expect(container.textContent).toContain('Inspect project structure (explore)')
    expect(container.textContent).toContain('Agent "Inspect project structure" completed')
    expect(container.textContent).toContain('Agent ID:child-1')
    expect(container.querySelector('strong')?.textContent).toBe('Findings')
    expect(container.textContent).not.toContain('<task')
    expect(container.textContent).not.toContain('"prompt"')
  })

  it('shows the requested prompt for a background subagent launch', () => {
    const { container } = renderACPToolPair(provider, {
      title: 'task',
      kind: 'think',
      rawInput: { description: 'Inspect project structure', subagent_type: 'explore', prompt: 'Inspect the entry points.' },
    }, {
      rawOutput: { metadata: { sessionId: 'child-1', background: true, jobId: 'child-1' } },
      content: [{ type: 'content', content: { type: 'text', text: '<task id="child-1" state="running">\n<summary>Background task started</summary>\n<task_result>\nModel-facing background instructions\n</task_result>\n</task>' } }],
    })
    expect(container.textContent).toContain('launched asynchronously')
    expect(container.textContent).toContain('Prompt')
    expect(container.textContent).toContain('Inspect the entry points.')
    expect(container.textContent).not.toContain('Model-facing background instructions')
  })

  it('renders a to-do update through one shared checklist', () => {
    const todos = [{ content: 'Inspect sample', status: 'in_progress', priority: 'medium' }, { content: 'Report findings', status: 'pending', priority: 'medium' }]
    const { container } = renderACPToolPair(provider, {
      title: 'todowrite',
      kind: 'other',
      rawInput: { todos },
    }, { rawOutput: { metadata: { todos } } })
    expect(container.querySelectorAll(`.${todoList}`)).toHaveLength(1)
    expect(container.textContent).toContain('Inspect sample')
    expect(container.textContent).toContain('Report findings')
    expect(container.textContent).not.toContain('Arguments')
  })

  it('keeps an MCP tool with a todos argument in the generic tool renderer', () => {
    const { container } = renderACPToolPair(provider, {
      title: 'calendar_sync',
      kind: 'other',
      rawInput: { todos: [{ content: 'Publish schedule', status: 'pending' }] },
    }, { content: [{ type: 'content', content: { type: 'text', text: 'Schedule published' } }] })
    expect(container.querySelectorAll(`.${todoList}`)).toHaveLength(0)
    expect(container.textContent).toContain('Schedule published')
  })

  it('shows the requested checklist while the tool is pending', () => {
    const { container } = renderTool({ sessionUpdate: 'tool_call', status: 'pending', title: 'todowrite', kind: 'other', rawInput: { todos: [{ content: 'Inspect sample', status: 'pending' }] } })
    expect(container.querySelector(`.${todoList}`)?.textContent).toContain('Inspect sample')
  })

  it('marks a cancelled native to-do as finished', () => {
    const { container } = renderTool({ title: 'todowrite', kind: 'other', rawOutput: { metadata: { todos: [{ content: 'Cancelled task', status: 'cancelled' }] } } })
    expect(container.querySelector(`.${todoList}`)?.textContent).toContain('Cancelled task')
    expect(container.querySelector('[data-task-checkbox="deleted"]')).not.toBeNull()
  })

  function renderTool(fields: Record<string, unknown>) {
    const tool = { sessionUpdate: 'tool_call_update', status: 'completed', toolCallId: 'tool', ...fields }
    return render(() => renderMessageContent(tool, { premeasureMode: true }, providerFor(provider)!.classify(input(tool)), provider))
  }

  it('renders directory entries from display metadata as a file list', () => {
    const { container } = renderTool({
      kind: 'read',
      title: 'read',
      rawInput: { filePath: '/project' },
      content: [{ type: 'content', content: { type: 'text', text: 'provider directory wrapper' } }],
      rawOutput: { metadata: { display: { type: 'directory', path: '/project', entries: ['a.ts', 'src/'], offset: 1, totalEntries: 2, truncated: false } } },
    })
    expect(container.textContent).toContain('2 entries')
    expect(container.textContent).toContain('a.ts')
    expect(container.textContent).toContain('src/')
    expect(container.textContent).not.toContain('provider directory wrapper')
  })

  it('renders each file from apply_patch metadata, including moves and deletions', () => {
    const first = '--- old.ts\n+++ new.ts\n@@ -7 +7 @@\n-beforeMove\n+afterMove\n'
    const second = '--- deleted.ts\n+++ deleted.ts\n@@ -3 +3,0 @@\n-removedLine\n'
    const { container } = renderTool({
      kind: 'edit',
      title: 'apply_patch',
      rawOutput: { metadata: { diff: first + second, files: [
        { filePath: '/project/old.ts', movePath: '/project/new.ts', type: 'move', patch: first },
        { filePath: '/project/deleted.ts', type: 'delete', patch: second },
      ] } },
    })
    expect(container.textContent).toContain('/project/old.ts')
    expect(container.textContent).toContain('/project/new.ts')
    expect(container.textContent).toContain('/project/deleted.ts')
    expect(container.textContent).toContain('afterMove')
    expect(container.textContent).toContain('removedLine')
  })

  it('copies the file text that display metadata supplies', () => {
    const tool = {
      sessionUpdate: 'tool_call_update',
      toolCallId: 'read',
      kind: 'read',
      status: 'completed',
      content: [{ type: 'content', content: { type: 'text', text: '<file>model-facing wrapper</file>' } }],
      rawOutput: { metadata: { display: { type: 'file', path: '/project/code.ts', text: 'const value = 42\n', lineStart: 1 } } },
    }
    const plugin = providerFor(provider)!
    const meta = plugin.toolResultMeta?.(plugin.classify(input(tool)), toolMessageInput(tool))
    expect(meta?.copyableContent()).toBe('const value = 42\n')
  })

  it('renders read display metadata with the correct line range', () => {
    const { container } = renderTool({
      kind: 'read',
      rawInput: { filePath: '/project/code.py', offset: 7 },
      content: [{ type: 'content', content: { type: 'text', text: 'answer = 42\nprint(answer)' } }],
      rawOutput: { metadata: { display: { type: 'file', path: '/project/code.py', text: 'answer = 42\nprint(answer)', lineStart: 7, lineEnd: 8, totalLines: 12 } } },
    })
    expect(container.textContent).toContain('answer = 42')
    expect(container.textContent).toContain('8')
  })

  it('preserves the matched lines when search metadata supplies the count', () => {
    const { container } = renderTool({
      kind: 'search',
      rawInput: { pattern: 'answer' },
      content: [{ type: 'content', content: { type: 'text', text: '/project/code.py:7:answer = 42' } }],
      rawOutput: { metadata: { matches: 1 } },
    })
    expect(container.textContent).toContain('answer = 42')
  })

  it('renders grouped native matches with one summary and both matching lines', () => {
    const { container } = renderTool({
      kind: 'search',
      title: 'grep',
      rawInput: { pattern: 'answer' },
      content: [{ type: 'content', content: { type: 'text', text: 'Found 2 matches\n/project/code.ts:\n  Line 7: const answer = 42\n\n  Line 8: console.log(answer)\n' } }],
      rawOutput: { metadata: { matches: 2, truncated: false } },
    })
    expect(container.textContent).toContain('2 matches in 1 file')
    expect(container.textContent).toContain('/project/code.ts:7:const answer = 42')
    expect(container.textContent).toContain('/project/code.ts:8:console.log(answer)')
    expect(container.textContent).not.toContain('Found 2 matches')
  })

  it('does not offer expansion for two normalized search rows', () => {
    const tool = {
      sessionUpdate: 'tool_call_update',
      toolCallId: 'search',
      status: 'completed',
      kind: 'search',
      title: 'grep',
      content: [{ type: 'content', content: { type: 'text', text: 'Found 2 matches\n/project/a:\n  Line 1: first\n\n/project/b:\n  Line 1: second\n' } }],
      rawOutput: { metadata: { matches: 2 } },
    }
    const plugin = providerFor(provider)!
    const meta = plugin.toolResultMeta?.(plugin.classify(input(tool)), toolMessageInput(tool))
    expect(meta?.collapsible).toBe(false)
  })

  it('prefers the applied patch with real line numbers over the replacement fragment', () => {
    const { container } = renderTool({
      kind: 'edit',
      rawInput: { filePath: '/project/code.py', oldString: '41', newString: '42' },
      content: [{ type: 'diff', path: '/project/code.py', oldText: '41', newText: '42' }],
      rawOutput: { metadata: { diff: '--- /project/code.py\n+++ /project/code.py\n@@ -7,2 +7,2 @@\n-answer = 41\n+answer = 42\n print(answer)\n' } },
    })
    expect(container.textContent).toContain('answer = 41')
    expect(container.textContent).toContain('answer = 42')
    expect(container.textContent).toContain('7')
  })
})
