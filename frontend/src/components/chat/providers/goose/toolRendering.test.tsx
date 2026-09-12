import { describe, expect, it } from 'vitest'
import { todoList } from '~/components/todo/TodoList.css'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { diffAdded, diffRemoved } from '../../diff/diffStyles.css'
import { acpTextContent, renderACPToolPair } from '../acp/testUtils'

import '../testMocks'
import './plugin'

describe('goose native tools', () => {
  it('renders a delegate report through the shared agent components', () => {
    const { container } = renderACPToolPair(AgentProvider.GOOSE, {
      kind: 'other',
      title: 'delegate',
      rawInput: { source: 'explore', instructions: 'Inspect the project' },
      _meta: { goose: { toolCall: { toolName: 'delegate', extensionName: 'summon' } } },
    }, { content: acpTextContent('**Findings**\n\nThe entry points exist.') })
    expect(container.textContent).toContain('Inspect the project (explore)')
    expect(container.textContent).toContain('Agent "Inspect the project" completed')
    expect(container.querySelector('strong')?.textContent).toBe('Findings')
    expect(container.textContent).not.toContain('Arguments')
  })

  it('shows the instructions for a background delegate instead of its model guidance', () => {
    const { container } = renderACPToolPair(AgentProvider.GOOSE, {
      kind: 'other',
      rawInput: { instructions: 'Inspect the project', async: true },
      _meta: { goose: { toolCall: { toolName: 'delegate', extensionName: 'summon' } } },
    }, { content: acpTextContent('Task 20260911_1 started in background: "Inspect the project"\nContinue with other work. When you need the result, use load(source: "20260911_1").') })
    expect(container.textContent).toContain('launched asynchronously')
    expect(container.textContent).toContain('Agent ID:20260911_1')
    expect(container.textContent).toContain('Inspect the project')
    expect(container.textContent).not.toContain('Continue with other work')
  })
  it('renders a subagent shell request with its actual command', () => {
    const { container } = renderACPToolPair(AgentProvider.GOOSE, {}, {
      status: 'in_progress',
      _meta: { toolNotification: { type: 'message', params: { data: { type: 'subagent_tool_request', subagent_id: 'child', tool_call: { name: 'developer__shell', arguments: { command: 'printf request-marker' } } } } } },
    })
    expect(container.textContent).toContain('printf request-marker')
    expect(container.textContent).not.toContain('exit')
  })

  it('shows a subagent edit request without claiming an applied diff', () => {
    const { container } = renderACPToolPair(AgentProvider.GOOSE, {}, {
      status: 'in_progress',
      _meta: { toolNotification: { type: 'message', params: { data: { type: 'subagent_tool_request', subagent_id: 'child', tool_call: { name: 'developer__edit', arguments: { path: 'sample.ts', before: 'before', after: 'after' } } } } } },
    })
    expect(container.textContent).toContain('sample.ts')
    expect(container.textContent).toContain('Requested changes')
    expect(container.querySelector(`.${diffRemoved}`)?.textContent).toContain('before')
    expect(container.querySelector(`.${diffAdded}`)?.textContent).toContain('after')
    expect(container.textContent).not.toContain('completed')
  })

  it('does not claim that the task list was cleared when the input is missing', () => {
    const { container } = renderACPToolPair(AgentProvider.GOOSE, {
      _meta: { goose: { toolCall: { toolName: 'todo__todo_write', extensionName: 'todo' } } },
    }, { content: acpTextContent('Tool outcome') })
    expect(container.textContent).not.toContain('cleared')
    expect(container.textContent).toContain('Tool outcome')
  })

  it('renders a simple task list through the common checklist', () => {
    const { container } = renderACPToolPair(AgentProvider.GOOSE, {
      title: 'todo: todo write',
      rawInput: { content: '- [x] Completed task\n- [ ] Pending task' },
      _meta: { goose: { toolCall: { toolName: 'todo__todo_write', extensionName: 'todo' } } },
    }, { content: acpTextContent('Updated') })
    expect(container.querySelector(`.${todoList}`)?.textContent).toContain('Completed task')
    expect(container.querySelector(`.${todoList}`)?.textContent).toContain('Pending task')
  })

  it('preserves headings and nested Markdown in a complex task list', () => {
    const { container } = renderACPToolPair(AgentProvider.GOOSE, {
      rawInput: { content: '## Tasks\n\n- [ ] **Inspect** files\n  - Keep this detail' },
      _meta: { goose: { toolCall: { toolName: 'todo__todo_write', extensionName: 'todo' } } },
    }, { content: acpTextContent('Updated') })
    expect(container.querySelector('h2')?.textContent).toBe('Tasks')
    expect(container.querySelector('strong')?.textContent).toBe('Inspect')
    expect(container.querySelector('li li')?.textContent).toContain('Keep this detail')
  })
  it('keeps the reported edit result separate from the requested changes', () => {
    const { container } = renderACPToolPair(AgentProvider.GOOSE, {
      title: 'edit · sample.py',
      rawInput: { path: 'sample.py', before: 'answer = 41', after: 'answer = 42' },
      _meta: { goose: { toolCall: { toolName: 'edit', extensionName: 'developer' } } },
    }, { content: acpTextContent('Edited sample.py (1 lines -> 1 lines)') })
    expect(container.querySelector(`.${diffRemoved}`)?.textContent).toContain('answer = 41')
    expect(container.querySelector(`.${diffAdded}`)?.textContent).toContain('answer = 42')
    expect(container.textContent).toContain('Edited sample.py (1 lines -> 1 lines)')
    expect(container.textContent?.match(/Requested changes/g)).toHaveLength(1)
    expect(container.querySelectorAll('[data-file-diff]')).toHaveLength(1)
  })

  it('renders a pure deletion with an empty after field', () => {
    const { container } = renderACPToolPair(AgentProvider.GOOSE, {
      rawInput: { path: 'sample.py', before: 'remove_this()', after: '' },
      _meta: { goose: { toolCall: { toolName: 'developer__edit', extensionName: 'developer' } } },
    }, { content: acpTextContent('Edited sample.py') })
    expect(container.querySelector(`.${diffRemoved}`)?.textContent).toContain('remove_this()')
    expect(container.querySelector(`.${diffAdded}`)).toBeNull()
  })

  it('keeps a failed write and labels its requested changes', () => {
    const { container } = renderACPToolPair(AgentProvider.GOOSE, {
      rawInput: { path: 'sample.py', content: 'unwritten()' },
      _meta: { goose: { toolCall: { toolName: 'write', extensionName: 'developer' } } },
    }, { status: 'failed', content: acpTextContent('Permission denied') })
    expect(container.textContent).toContain('Permission denied')
    expect(container.textContent).toContain('Error')
    expect(container.querySelector(`.${diffAdded}`)?.textContent).toContain('unwritten()')
    expect(container.textContent?.match(/Requested changes/g)).toHaveLength(1)
  })
})

// A FAILED Goose shell call carries no `rawOutput` at all. Goose states the exit
// code in a content block of its own, ahead of the output, and that block is the
// only place it exists. Without reading it the row says "Error" where Codex,
// OpenCode, Pi and GitHub Copilot all say "Error (exit 1)" for the same failure.
describe('goose shell exit code', () => {
  const shell = {
    kind: 'execute',
    title: 'List a missing directory',
    rawInput: { command: 'ls no-such/', description: 'List a missing directory' },
    _meta: { goose: { toolCall: { toolName: 'developer__shell', extensionName: 'developer' } } },
  }
  /** The two blocks Goose actually sends: the code, then the output. */
  const failureContent = [
    { type: 'content', content: { type: 'text', text: 'exit code: 1' } },
    { type: 'content', content: { type: 'text', text: 'ls: no-such/: No such file or directory\n' } },
  ]

  it('reads the code from the block Goose puts ahead of the output', () => {
    const { container } = renderACPToolPair(AgentProvider.GOOSE, shell, { status: 'failed', content: failureContent })
    expect(container.textContent).toContain('Error (exit 1)')
    expect(container.textContent).toContain('ls: no-such/: No such file or directory')
  })

  // The code belongs in the label and nowhere else. Leaving the block in the body
  // shows it twice, which is the defect ZCode had.
  it('shows the code once, in the label', () => {
    const { container } = renderACPToolPair(AgentProvider.GOOSE, shell, { status: 'failed', content: failureContent })
    expect(container.textContent).not.toContain('exit code: 1')
  })

  // `rawOutput.exit_code` is the structured statement when Goose sends one, and it
  // stays the one the label uses.
  it('prefers the structured code when both are present', () => {
    const { container } = renderACPToolPair(AgentProvider.GOOSE, shell, {
      status: 'failed',
      rawOutput: { exit_code: 2, stdout: '', stderr: 'boom' },
      content: failureContent,
    })
    expect(container.textContent).toContain('Error (exit 2)')
    expect(container.textContent).toContain('boom')
  })

  it('leaves a successful command with no exit code in its body', () => {
    const { container } = renderACPToolPair(AgentProvider.GOOSE, shell, {
      status: 'completed',
      content: [{ type: 'content', content: { type: 'text', text: 'hello\n' } }],
    })
    expect(container.textContent).toContain('hello')
    expect(container.textContent).not.toContain('exit code')
  })
})
