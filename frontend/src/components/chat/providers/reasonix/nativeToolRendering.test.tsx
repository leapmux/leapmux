import { describe, expect, it } from 'vitest'
import { todoList } from '~/components/todo/TodoList.css'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { diffAdded, diffRemoved } from '../../diff/diffStyles.css'
import { acpTextContent, renderACPToolPair } from '../acp/testUtils'

import '../testMocks'
import './plugin'

describe('reasonix capability tools', () => {
  it('renders the subagent outcome behind a recovered error headline', () => {
    const { container } = renderACPToolPair(AgentProvider.REASONIX, {
      title: 'task',
      kind: 'other',
      rawInput: { description: 'Inspect sample', prompt: 'Read sample.py' },
    }, { status: 'failed', content: acpTextContent('context canceled') }, {
      rawOutput: { reasonix: { role: 'tool', tool_call_id: 'call', name: 'task', content: 'error: context canceled\nSubagent outcome: status=cancelled retryable=false\n\nFinal answer:\n- **Partial finding**' } },
    })
    expect(container.textContent).toContain('Agent "Inspect sample" stopped')
    expect(container.textContent).toContain('context canceled')
    expect(container.querySelector('li strong')?.textContent).toBe('Partial finding')
    expect(container.textContent).not.toContain('Subagent outcome:')
  })

  it.each([
    'Subagent outcome: status=failed retryable=false\n\nFinal answer:\nQuoted report',
    'Started background task "example-job" (An example).',
  ])('keeps successful read-only output literal: %s', (output) => {
    const { container } = renderACPToolPair(AgentProvider.REASONIX, {
      kind: 'other',
      title: 'read_only_task',
      rawInput: { description: 'Inspect sample', prompt: 'Read a protocol example' },
    }, { content: acpTextContent(output) })
    expect(container.textContent).toContain('Agent "Inspect sample" completed')
    expect(container.textContent).toContain(output.split('\n')[0])
  })

  it.each([false, true])('uses the shared agent body for read_only_task (wrapped: %s)', (wrapped) => {
    const args = { description: 'Inspect sample', prompt: 'Read sample.py' }
    const { container } = renderACPToolPair(AgentProvider.REASONIX, {
      kind: 'other',
      title: wrapped ? 'use_capability' : 'read_only_task',
      rawInput: wrapped ? { action: 'call', capability_id: 'tool:read_only_task', arguments: args } : args,
    }, { content: acpTextContent('- **Read-only finding**') })
    expect(container.textContent).toContain('Agent "Inspect sample" completed')
    expect(container.querySelector('li strong')?.textContent).toBe('Read-only finding')
    expect(container.textContent).not.toContain('"arguments"')
  })

  it('renders a structured subagent outcome and its report with shared components', () => {
    const { container } = renderACPToolPair(AgentProvider.REASONIX, {
      kind: 'other',
      title: 'task',
      rawInput: { description: 'Inspect the project', prompt: 'Read the entry points.', profile: 'explore' },
    }, { content: acpTextContent('Subagent reference: sa_example\nSubagent outcome: status=completed retryable=false\n\nTo continue this same subagent transcript in a later call, pass this ref as `continue_from`. Start a fresh subagent when the next task is independent.\n\nFinal answer:\n**Findings**\n\nThe entry points exist.') })
    expect(container.textContent).toContain('Inspect the project (explore)')
    expect(container.textContent).toContain('Agent "Inspect the project" completed')
    expect(container.textContent).toContain('Agent ID:sa_example')
    expect(container.querySelector('strong')?.textContent).toBe('Findings')
    expect(container.textContent).not.toContain('Subagent outcome:')
    expect(container.textContent).not.toContain('To continue this same')
  })
  it('renders directory entries with their file sizes', () => {
    const { container } = renderACPToolPair(AgentProvider.REASONIX, {
      title: 'ls',
      kind: 'read',
      rawInput: { path: '/project' },
    }, { content: acpTextContent('a.ts\t42\nsrc/\n') })
    expect(container.textContent).toContain('2 entries')
    expect(container.textContent).toContain('a.ts')
    expect(container.textContent).toContain('42 B')
    expect(container.textContent).toContain('src/')
  })

  it('keeps a directory truncation notice separate from its entries', () => {
    const { container } = renderACPToolPair(AgentProvider.REASONIX, {
      title: 'ls',
      kind: 'read',
      rawInput: { path: '/project' },
    }, { content: acpTextContent('a.ts\t42\n…(100 more chars truncated)') })
    expect(container.textContent).toContain('1 entry')
    expect(container.textContent).not.toContain('2 entries')
    expect(container.textContent).toContain('100 more chars truncated')
  })

  it('renders the structured todo input through the common checklist', () => {
    const { container } = renderACPToolPair(AgentProvider.REASONIX, {
      title: 'todo_write',
      kind: 'edit',
      rawInput: { todos: [{ content: 'Inspect code', status: 'in_progress' }, { content: 'Run checks', status: 'pending' }] },
    }, { content: acpTextContent('Todos updated') })
    expect(container.querySelector(`.${todoList}`)?.textContent).toContain('Inspect code')
    expect(container.querySelector(`.${todoList}`)?.textContent).toContain('Run checks')
  })

  it('renders every replacement from a multi_edit request', () => {
    const { container } = renderACPToolPair(AgentProvider.REASONIX, {
      title: 'multi_edit',
      kind: 'edit',
      rawInput: { path: '/project/file.ts', edits: [{ old_string: 'firstBefore', new_string: 'firstAfter' }, { old_string: 'secondBefore', new_string: 'secondAfter' }] },
    }, { content: acpTextContent('Applied edits') })
    expect(container.querySelectorAll(`.${diffAdded}`)).toHaveLength(2)
    expect(container.textContent).toContain('firstAfter')
    expect(container.textContent).toContain('secondAfter')
    expect(container.textContent).not.toContain('2 files changed')
  })

  it('prefers the actual fuzzy replacement receipt over the requested text', () => {
    const { container } = renderACPToolPair(AgentProvider.REASONIX, {
      title: 'edit_file',
      kind: 'edit',
      rawInput: { path: '/project/file.ts', old_string: 'requestedBefore', new_string: 'requestedAfter' },
    }, { content: acpTextContent('edited /project/file.ts\nActual replacement receipt after write:\n@@ replacement 1 of 1 (1 occurrence(s), fuzzy match) @@\n-actualBefore\n+actualAfter\n') })
    expect(container.querySelector(`.${diffRemoved}`)?.textContent).toContain('actualBefore')
    expect(container.querySelector(`.${diffAdded}`)?.textContent).toContain('actualAfter')
    expect(container.textContent).not.toContain('requestedBefore')
    expect(container.textContent).toContain('Fuzzy match')
  })

  it.each(['delete_range', 'delete_symbol'])('renders the actual deletion diff from %s', (title) => {
    const { container } = renderACPToolPair(AgentProvider.REASONIX, {
      title,
      kind: 'delete',
      rawInput: { path: '/project/file.ts', start_anchor: 'delete', end_anchor: 'end' },
    }, { content: acpTextContent('--- a/project/file.ts\n+++ b/project/file.ts\n@@ -4,3 +4,1 @@\n-removedFirst\n-removedSecond\n remaining\n') })
    expect(container.querySelectorAll(`.${diffRemoved}`)).toHaveLength(2)
    expect(container.textContent).toContain('removedFirst')
  })

  it('renders a successful move with both paths', () => {
    const { container } = renderACPToolPair(AgentProvider.REASONIX, {
      title: 'move_file',
      kind: 'edit',
      rawInput: { source_path: '/project/old.ts', destination_path: '/project/new.ts' },
    }, { content: acpTextContent('moved') })
    expect(container.textContent).toContain('/project/old.ts')
    expect(container.textContent).toContain('/project/new.ts')
  })

  it('renders fetched Markdown when no HTTP status is supplied', () => {
    const { container } = renderACPToolPair(AgentProvider.REASONIX, {
      title: 'web_fetch',
      kind: 'other',
      rawInput: { url: 'https://example.com' },
    }, { content: acpTextContent('## Page title\n\n**Page body**') })
    expect(container.querySelector('h2')?.textContent).toBe('Page title')
    expect(container.querySelector('strong')?.textContent).toBe('Page body')
    expect(container.textContent).not.toContain('200')
  })
  it('renders a complete saved read when the protocol result is truncated', () => {
    const { container } = renderACPToolPair(AgentProvider.REASONIX, {
      title: 'read_file',
      kind: 'read',
      rawInput: { path: 'sample.py' },
    }, {
      content: acpTextContent('1→first line\n…(22 more chars truncated)'),
    }, {
      rawOutput: { reasonix: {
        role: 'tool',
        tool_call_id: 'call',
        name: 'read_file',
        content: '1→first line\n2→FULL_SECOND_LINE',
        read_result: { source: { canonical_path: '/project/sample.py' }, eof: true, source_end: 2 },
      } },
    })
    expect(container.textContent).toContain('FULL_SECOND_LINE')
    expect(container.textContent).not.toContain('chars truncated')
  })

  it('ignores a saved result for a different tool call', () => {
    const { container } = renderACPToolPair(AgentProvider.REASONIX, {
      title: 'read_file',
      kind: 'read',
      rawInput: { path: 'sample.py' },
    }, {
      content: acpTextContent('1→original line'),
    }, {
      rawOutput: { reasonix: { role: 'tool', tool_call_id: 'another-call', name: 'read_file', content: '1→foreign line' } },
    })
    expect(container.textContent).toContain('original line')
    expect(container.textContent).not.toContain('foreign line')
  })

  it('renders a wrapped glob as a file search', () => {
    const { container } = renderACPToolPair(AgentProvider.REASONIX, {
      title: 'use_capability',
      kind: 'other',
      rawInput: { action: 'call', capability_id: 'tool:glob', arguments: { pattern: '*.py' } },
    }, { content: acpTextContent('/project/first.py\n/project/second.py') })
    expect(container.textContent).toContain('Found 2 files')
    expect(container.textContent).toContain('*.py')
    expect(container.textContent).not.toContain('use_capability')
  })

  it('renders a wrapped grep with its matching lines', () => {
    const { container } = renderACPToolPair(AgentProvider.REASONIX, {
      title: 'use_capability',
      kind: 'other',
      rawInput: { action: 'call', capability_id: 'tool:grep', arguments: { pattern: 'answer', path: '/project' } },
    }, { content: acpTextContent('/project/a.py:1:answer = 42') })
    expect(container.textContent).toContain('1 match')
    expect(container.textContent).toContain('/project/a.py:1:answer = 42')
    expect(container.textContent).not.toContain('use_capability')
  })

  it('uses the wrapped MCP server and tool names with the inner arguments', () => {
    const { container } = renderACPToolPair(AgentProvider.REASONIX, {
      title: 'use_capability',
      kind: 'other',
      rawInput: { action: 'call', capability_id: 'mcp-tool:docs/search', arguments: { query: 'sessions' } },
    }, { content: acpTextContent('Session documentation') })
    expect(container.textContent).toContain('docs / search')
    expect(container.textContent).toContain('sessions')
    expect(container.textContent).toContain('Session documentation')
    expect(container.textContent).not.toContain('capability_id')
  })
})
