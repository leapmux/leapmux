import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { CODEWHALE_TOOL } from '~/generated/contracts/codewhale-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { testMessageSources } from '~/test-support/messageRenderSources'
import { renderMessageContent } from '../../messageContentRenderer'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import { requestSide, toolCompleted, toolStarted } from './toolResults.fixtures'
import './plugin'
import '../testMocks'

const provider = () => providerFor(AgentProvider.CODEWHALE)!

/** Draw one finished call through the shared renderer, with its opening frame beside it. */
function renderResult(toolName: string, args: Record<string, unknown>, detail: string, metadata: Record<string, unknown> = {}) {
  const request = requestSide(toolStarted(toolName, args))
  const end = toolCompleted(toolName, args, detail, metadata)
  const parsed = input(end, undefined, AgentProvider.CODEWHALE)
  const category = provider().transcript.classify({ ...parsed, spanType: toolName })
  return render(() => renderMessageContent(end, {
    workingDir: '/project',
    spanType: toolName,
    premeasureMode: true,
    getMessageUiState: () => true,
    sources: testMessageSources({ current: () => parsed, request: () => request }),
  }, category, AgentProvider.CODEWHALE))
}

describe('codewhale tool rendering', () => {
  it('draws a command with its output', () => {
    const { container } = renderResult(CODEWHALE_TOOL.Bash, { command: 'ls -1' }, 'a.ts\nb.ts\n', { exit_code: 0 })
    expect(container.textContent).toContain('ls -1')
    expect(container.textContent).toContain('b.ts')
  })

  // The runtime's diff carries context lines that the edit's arguments never state,
  // so a context line on screen proves that the row drew the LANDED change.
  it('draws the diff an edit landed', () => {
    const { container } = renderResult(CODEWHALE_TOOL.Edit, { path: 'note.txt', edits: [{ oldText: 'hi', newText: 'hello' }] }, 'Replaced 1 block', {
      mutation: { diff: '--- a/note.txt\n+++ b/note.txt\n@@ -1,2 +1,2 @@\n a context line\n-hi\n+hello\n', files: [{ path: 'note.txt', outcome: 'updated' }], renames: [] },
    })
    expect(container.textContent).toContain('note.txt')
    expect(container.textContent).toContain('hello')
    expect(container.textContent).toContain('a context line')
  })

  it('draws the checklist a to-do call states', () => {
    const { container } = renderResult(CODEWHALE_TOOL.TodoWrite, { todos: [{ content: 'Run the tests', status: 'pending' }] }, 'Todo list updated')
    expect(container.textContent).toContain('Run the tests')
  })

  // A partial update sends one item, and the runtime answers with the whole list it
  // kept. The item that only the kept list holds proves which list the row drew.
  it('draws the checklist the runtime kept over the one the call sent', () => {
    const { container } = renderResult(CODEWHALE_TOOL.ChecklistUpdate, { todos: [{ content: 'Run the tests', status: 'completed' }] }, 'Checklist updated', {
      task_updates: { checklist: { items: [{ id: 1, content: 'Read the code', status: 'completed' }, { id: 2, content: 'Run the tests', status: 'completed' }] } },
    })
    expect(container.textContent).toContain('Read the code')
    expect(container.textContent).toContain('Run the tests')
  })

  // Both rows are on screen, as they are in a live transcript. The runtime
  // redacts a question's answers from its result, so the result row hides and
  // the request row draws the question. An empty result row would hold every
  // later row of the transcript behind its measurement.
  it('draws the question a call asked on its request row, and hides the redacted result row', () => {
    const args = { questions: [{ id: 'c', question: 'Which color?', options: [{ label: 'Red' }] }] }
    const start = toolStarted(CODEWHALE_TOOL.RequestUserInput, args)
    const end = toolCompleted(CODEWHALE_TOOL.RequestUserInput, args, 'User input submitted')
    const startParsed = input(start, undefined, AgentProvider.CODEWHALE)
    const endParsed = input(end, undefined, AgentProvider.CODEWHALE)
    const both = { request: true, result: true }
    const request = render(() => renderMessageContent(start, {
      workingDir: '/project',
      spanType: CODEWHALE_TOOL.RequestUserInput,
      premeasureMode: true,
      getMessageUiState: () => true,
      sources: testMessageSources({ current: () => startParsed, result: () => endParsed, role: () => 'request', visibleRows: () => both }),
    }, provider().transcript.classify({ ...startParsed, spanType: CODEWHALE_TOOL.RequestUserInput }), AgentProvider.CODEWHALE))
    expect(request.container.textContent).toContain('Which color?')

    const result = render(() => renderMessageContent(end, {
      workingDir: '/project',
      spanType: CODEWHALE_TOOL.RequestUserInput,
      premeasureMode: true,
      getMessageUiState: () => true,
      sources: testMessageSources({ current: () => endParsed, request: () => requestSide(start), role: () => 'result', visibleRows: () => both }),
    }, provider().transcript.classify({ ...endParsed, spanType: CODEWHALE_TOOL.RequestUserInput }), AgentProvider.CODEWHALE))
    expect(result.container.textContent).toBe('')
  })

  it('draws the child a subagent call launched', () => {
    const { container } = renderResult(CODEWHALE_TOOL.Agent, { action: 'start', name: 'counter', prompt: 'Count the files.' }, JSON.stringify({ name: 'counter', agent_id: 'agent_1', status: 'running' }))
    expect(container.textContent).toContain('counter')
    // The arguments also state the name. The child's id is in the answer alone.
    expect(container.textContent).toContain('agent_1')
  })
})
