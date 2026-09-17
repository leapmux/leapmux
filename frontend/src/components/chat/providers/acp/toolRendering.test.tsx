import type { RenderContext } from '../../messageRenderers'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { testMessageContext } from '~/test-support/messageContext'
import { makeMessage, rawContent } from '~/test-support/messageFactory'
import { testMessageSources } from '~/test-support/messageRenderSources'
import { providerToolMeta } from '~/test-support/toolCallIr'
import { MessageBubble } from '../../MessageBubble'
import { renderMessageContent } from '../../rowRenderers'
import { toolBodyBorder, toolUseHeader } from '../../toolStyles.css'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import { renderACPToolPair } from './testUtils'
import '../opencode/plugin'
import '../kilo/plugin'
import '../goose/plugin'
import '../reasonix/plugin'
import '../cursor/plugin'
import '../copilot/plugin'
import '../testMocks'

function renderTool(tool: Record<string, unknown>, context?: RenderContext) {
  const plugin = providerFor(AgentProvider.OPENCODE)!
  const category = plugin?.transcript.classify(input(tool))
  return render(() => renderMessageContent(tool, context, category, AgentProvider.OPENCODE))
}

describe('acp tool rendering', () => {
  it.each([
    [AgentProvider.OPENCODE, 'todowrite'],
    [AgentProvider.KILO, 'todowrite'],
    [AgentProvider.REASONIX, 'todo_write'],
  ])('composes the task-count header for a %s to-do pair', (provider, title) => {
    const todos = [{ content: 'Inspect sample', status: 'pending' }]
    const { container } = renderACPToolPair(provider, {
      title,
      kind: 'other',
      rawInput: { todos },
    }, {
      rawOutput: { metadata: { todos } },
      content: [{ type: 'content', content: { type: 'text', text: JSON.stringify(todos) } }],
    })
    // The REQUEST row's header: the renderer composes it from the list, and the
    // frame's own tool word ('todowrite') must not stand in for that count.
    expect(container.textContent).toContain('1 task')
    expect(container.textContent).not.toContain(title)
    expect(container.querySelector('[data-task-checkbox="pending"]')).not.toBeNull()
  })

  it.each(['completed'])('shows one requested diff and retains a %s edit result', (status) => {
    const { container } = renderACPToolPair(AgentProvider.OPENCODE, {
      title: 'edit',
      kind: 'edit',
      rawInput: { filePath: '/project/example.ts', oldString: 'before', newString: 'after' },
    }, {
      status,
      content: [{ type: 'content', content: { type: 'text', text: 'No file changes occurred.' } }],
    })
    expect(container.textContent).toContain('No file changes occurred.')
    expect(container.textContent?.match(/Requested changes/g)).toHaveLength(1)
    expect(container.querySelectorAll('[data-file-diff]')).toHaveLength(1)
    expect(container.querySelector('[data-file-diff]')?.textContent).toContain('before')
    expect(container.querySelector('[data-file-diff]')?.textContent).toContain('after')
  })

  it('renders retained protocol fields and terminal output without changing the original event', () => {
    const original = { sessionUpdate: 'tool_call_update', toolCallId: 'terminal-call', status: 'completed', content: [{ type: 'terminal', terminalId: 'terminal' }] }
    const message = makeMessage({
      agentProvider: AgentProvider.GOOSE,
      spanId: 'terminal-call',
      content: rawContent(original),
      supplementalContent: rawContent({ provider: { ...original, protocol: { kind: 'execute', rawInput: { command: 'printf recovered-output' } }, terminals: { terminal: { output: 'recovered-output', exitCode: 0, truncated: false } } } }),
    })
    const { container } = render(() => <PreferencesProvider><MessageBubble message={message} premeasureMode /></PreferencesProvider>)
    expect(container.textContent).toContain('recovered-output')
    expect(container.textContent).not.toContain('[no output]')
    expect(container.textContent).not.toContain('Terminal terminal')
    expect(new TextDecoder().decode(message.content)).toBe(JSON.stringify(original))
  })

  it('renders every recovered terminal with its own exit status', () => {
    const original = { sessionUpdate: 'tool_call_update', toolCallId: 'terminal-call', status: 'completed', kind: 'execute', content: [{ type: 'terminal', terminalId: 'first' }, { type: 'terminal', terminalId: 'second' }] }
    const message = makeMessage({ agentProvider: AgentProvider.OPENCODE, spanId: 'terminal-call', content: rawContent(original), supplementalContent: rawContent({ provider: { ...original, terminals: { first: { output: 'first output', exitCode: 0 }, second: { output: 'second output', exitCode: 2 } } } }) })
    const { container } = render(() => <PreferencesProvider><MessageBubble message={message} premeasureMode /></PreferencesProvider>)
    expect(container.textContent).toContain('first output')
    expect(container.textContent).toContain('second output')
    expect(container.textContent).toContain('Error (exit 2)')
  })

  it('ignores a recovered terminal that the tool did not identify', () => {
    const original = { sessionUpdate: 'tool_call_update', toolCallId: 'terminal-call', status: 'completed', kind: 'execute', content: [{ type: 'terminal', terminalId: 'first' }] }
    const message = makeMessage({ agentProvider: AgentProvider.OPENCODE, spanId: 'terminal-call', content: rawContent(original), supplementalContent: rawContent({ provider: { ...original, terminals: { unrelated: { output: 'foreign output', exitCode: 0 } } } }) })
    const { container } = render(() => <PreferencesProvider><MessageBubble message={message} premeasureMode /></PreferencesProvider>)
    expect(container.textContent).not.toContain('foreign output')
    expect(container.textContent).toContain('Terminal first')
  })
  it('uses the common write header for OpenCode file creation', () => {
    const request = { sessionUpdate: 'tool_call', toolCallId: 'write', kind: 'edit', title: 'write', status: 'pending', rawInput: { filePath: '/project/created.txt', content: 'First line\nSecond line\n' } }
    const result = { sessionUpdate: 'tool_call_update', toolCallId: 'write', status: 'completed', rawOutput: { metadata: { exists: false, filepath: '/project/created.txt' } } }
    const { container } = renderTool(request, { premeasureMode: true, workingDir: '/project', sources: testMessageSources({ result: () => input(result) }) })
    expect(container.querySelector(`.${toolUseHeader}`)?.textContent).toBe('created.txt (2 lines)')
  })

  it('uses the common file search title without quoting the glob pattern', () => {
    const request = { sessionUpdate: 'tool_call', toolCallId: 'search', kind: 'search', title: 'glob', status: 'pending', rawInput: { pattern: '*.ts', path: '/project/src' } }
    const result = { sessionUpdate: 'tool_call_update', toolCallId: 'search', status: 'completed', rawOutput: { metadata: { count: 1 } }, content: [{ type: 'content', content: { type: 'text', text: '/project/src/a.ts' } }] }
    const { container } = renderTool(request, { premeasureMode: true, workingDir: '/project', sources: testMessageSources({ result: () => input(result) }) })
    expect(container.querySelector(`.${toolUseHeader}`)?.textContent).toBe('*.ts src')
  })

  it('puts the content search path below the pattern', () => {
    const { container } = renderTool({ sessionUpdate: 'tool_call', toolCallId: 'search', kind: 'search', title: 'grep', status: 'pending', rawInput: { pattern: 'answer', path: '/project/src' } }, { premeasureMode: true, workingDir: '/project' })
    expect(container.querySelector(`.${toolUseHeader}`)?.textContent).toBe('"answer"')
    expect(container.querySelector(`.${toolBodyBorder}`)?.textContent).toBe('src')
  })

  it.each([
    { title: 'bash', rawInput: { command: 'printf hello' }, expected: 'Run command' },
    { title: 'printf hello', rawInput: { command: 'printf hello' }, expected: 'Run command' },
    { title: 'bash', rawInput: { command: 'printf hello', description: 'Print a greeting' }, expected: 'Print a greeting' },
  ])('uses the common command header for $title', ({ title, rawInput, expected }) => {
    const { container } = renderTool({ sessionUpdate: 'tool_call', toolCallId: 'command', kind: 'execute', status: 'pending', title, rawInput })
    expect(container.querySelector(`.${toolUseHeader}`)?.textContent).toBe(expected)
    expect(container.textContent?.match(/printf hello/g)).toHaveLength(1)
  })

  // GitHub Copilot is NOT in this list, and its absence is the point: it runs on its
  // native protocol, so an Agent Client Protocol `tool_call_update` means nothing to its
  // plugin. The case passed here for the wrong reason -- the last-resort renderer used to
  // print the whole frame, so the assertion found `File content` inside the JSON it
  // dumped. Copilot's own paired read is covered in `copilot/toolRendering.test.tsx`.
  it.each([AgentProvider.OPENCODE, AgentProvider.KILO, AgentProvider.GOOSE, AgentProvider.REASONIX, AgentProvider.CURSOR])('renders a paired read result without another header or body border (%s)', (provider) => {
    const request = { sessionUpdate: 'tool_call', toolCallId: 'read', kind: 'read', status: 'pending', rawInput: { path: '/project/README.md' } }
    const result = { sessionUpdate: 'tool_call_update', toolCallId: 'read', status: 'completed', content: [{ type: 'content', content: { type: 'text', text: '1\tFile content' } }] }
    const plugin = providerFor(provider)!
    const { container } = render(() => renderMessageContent(result, {
      premeasureMode: true,
      sources: testMessageSources({ request: () => input(request) }),
    }, plugin?.transcript.classify(input(result)), provider))
    expect(container.textContent).toContain('File content')
    expect(container.querySelector(`.${toolUseHeader}`)).toBeNull()
    expect(container.querySelector(`.${toolBodyBorder}`)).toBeNull()
  })

  // Every Agent Client Protocol provider, not the shared path once: Cursor adapts the
  // presentation, and an adapter can lose a header as easily as gain one. The row is
  // credited per provider in the parity matrix, so the case runs per provider.
  it.each([
    [AgentProvider.OPENCODE, undefined],
    [AgentProvider.OPENCODE, 'different-call'],
    [AgentProvider.KILO, undefined],
    [AgentProvider.GOOSE, undefined],
    [AgentProvider.REASONIX, undefined],
    [AgentProvider.CURSOR, undefined],
  ])('keeps a result header when the request does not match (%s, request %s)', (provider, requestId) => {
    const result = { sessionUpdate: 'tool_call_update', toolCallId: 'read', kind: 'read', status: 'completed', rawInput: { path: '/project/README.md' }, content: [{ type: 'content', content: { type: 'text', text: '1\tFile content' } }] }
    const plugin = providerFor(provider)!
    const { container } = render(() => renderMessageContent(result, {
      premeasureMode: true,
      sources: testMessageSources({ request: () => requestId ? input({ sessionUpdate: 'tool_call', toolCallId: requestId, status: 'pending' }) : undefined }),
    }, plugin?.transcript.classify(input(result)), provider))
    expect(container.querySelector(`.${toolUseHeader}`)?.textContent).toContain('README.md')
    expect(container.textContent).toContain('File content')
  })

  // Which related half an Agent Client Protocol row wants, per provider rather than
  // for the shared path once: a request with an empty rawInput draws its body from the
  // result, and a result draws its name from the request.
  it.each([AgentProvider.OPENCODE, AgentProvider.KILO, AgentProvider.GOOSE, AgentProvider.REASONIX, AgentProvider.CURSOR])('declares the related half per provider (%s)', (provider) => {
    const plugin = providerFor(provider)!
    expect(plugin?.transcript.relatedMessages?.(input({ sessionUpdate: 'tool_call', toolCallId: 'read', kind: 'read', title: 'read', status: 'pending', rawInput: {} }))).toEqual(['result'])
    expect(plugin?.transcript.relatedMessages?.(input({ sessionUpdate: 'tool_call_update', toolCallId: 'read', status: 'completed', content: [] }))).toEqual(['request'])
  })

  // The completion that names NO tool of its own. With no request beside it and no
  // span type, the row has only its content to show, and it must show that rather than
  // a tool name it never read.
  it.each([AgentProvider.OPENCODE, AgentProvider.KILO, AgentProvider.GOOSE, AgentProvider.REASONIX, AgentProvider.CURSOR])('shows an unmatched completion by its content alone (%s)', (provider) => {
    const result = { sessionUpdate: 'tool_call_update', toolCallId: 'orphan', status: 'completed', content: [{ type: 'content', content: { type: 'text', text: 'recovered output' } }] }
    const plugin = providerFor(provider)!
    const { container } = render(() => renderMessageContent(result, {
      premeasureMode: true,
      sources: testMessageSources({ request: () => undefined }),
    }, plugin?.transcript.classify(input(result)), provider))
    expect(container.textContent).toContain('recovered output')
    for (const invented of ['Run command', 'README', 'Read file', 'Search'])
      expect(container.textContent).not.toContain(invented)
  })

  it('recovers a read header from matching result metadata without inventing a requested range', () => {
    const request = { sessionUpdate: 'tool_call', toolCallId: 'read', kind: 'read', title: 'read', status: 'pending', rawInput: {} }
    const result = { sessionUpdate: 'tool_call_update', toolCallId: 'read', status: 'completed', rawOutput: { metadata: { display: { type: 'file', path: '/project/README.md', text: 'File content', lineStart: 1, lineEnd: 1 } } } }
    const { container } = renderTool(request, { premeasureMode: true, workingDir: '/project', sources: testMessageSources({ result: () => input(result) }) })
    expect(container.querySelector(`.${toolUseHeader}`)?.textContent).toBe('README.md')
    expect(container.textContent).not.toContain('File content')
    expect(providerFor(AgentProvider.OPENCODE)!.transcript.relatedMessages?.(input(request))).toEqual(['result'])
  })

  it('loads result metadata when partial read input has no file path', () => {
    const request = { sessionUpdate: 'tool_call', toolCallId: 'read', kind: 'read', title: 'read', status: 'pending', rawInput: { offset: 7, limit: 2 } }
    expect(providerFor(AgentProvider.OPENCODE)!.transcript.relatedMessages?.(input(request))).toEqual(['result'])
  })

  it('uses a protocol file location without a related-message request', () => {
    const request = { sessionUpdate: 'tool_call', toolCallId: 'read', kind: 'read', title: 'read', status: 'pending', rawInput: { offset: 7 }, locations: [{ path: '/project/sample.ts', line: 7 }] }
    const { container } = renderTool(request, { premeasureMode: true, workingDir: '/project' })
    expect(container.querySelector(`.${toolUseHeader}`)?.textContent).toBe('sample.ts (Line 7–)')
    expect(providerFor(AgentProvider.OPENCODE)!.transcript.relatedMessages?.(input(request))).toEqual([])
  })

  it('does not use another call to supply a request header', () => {
    const request = { sessionUpdate: 'tool_call', toolCallId: 'read', kind: 'read', title: 'Read File', status: 'pending', rawInput: {} }
    const result = { sessionUpdate: 'tool_call_update', toolCallId: 'other', status: 'completed', rawInput: { filePath: '/project/unrelated.md' } }
    const { container } = renderTool(request, { premeasureMode: true, sources: testMessageSources({ result: () => input(result) }) })
    expect(container.textContent).toContain('Read File')
    expect(container.textContent).not.toContain('unrelated.md')
  })

  it('shows statistics from the applied diff instead of a single replacement fragment', () => {
    const { container } = renderTool({
      sessionUpdate: 'tool_call_update',
      toolCallId: 'edit',
      kind: 'edit',
      status: 'completed',
      rawInput: { filePath: '/project/file.ts', oldString: 'before', newString: 'after' },
      content: [{ type: 'diff', path: '/project/file.ts', oldText: 'before\nbefore\nbefore\n', newText: 'after\nafter\nafter\n' }],
    }, { premeasureMode: true })
    expect(container.querySelector(`.${toolUseHeader}`)?.textContent).toContain('+3')
    expect(container.querySelector(`.${toolUseHeader}`)?.textContent).toContain('-3')
  })

  it('uses separate supplemental input to render an unchanged request', () => {
    const original = { sessionUpdate: 'tool_call', toolCallId: 'command', status: 'pending', kind: 'execute', title: 'Run checks', rawInput: {} }
    const message = makeMessage({
      agentProvider: AgentProvider.OPENCODE,
      spanId: 'command',
      content: rawContent(original),
      supplementalContent: rawContent({ provider: { ...original, rawInput: { command: 'npm test -- --runInBand' } } }),
    })
    const { container } = render(() => <PreferencesProvider><MessageBubble message={message} premeasureMode /></PreferencesProvider>)
    expect(container.textContent).toContain('npm test -- --runInBand')
    expect(new TextDecoder().decode(message.content)).toBe(JSON.stringify(original))
  })

  it('resolves a result request through the message host', () => {
    const request = {
      sessionUpdate: 'tool_call',
      toolCallId: 'edit',
      kind: 'edit',
      rawInput: { filePath: '/project/file.ts', oldString: 'hostBefore', newString: 'hostAfter' },
    }
    const message = makeMessage({
      agentProvider: AgentProvider.OPENCODE,
      spanId: 'edit',
      id: 'edit-result',
      seq: 2n,
      spanType: 'edit',
      content: rawContent({ sessionUpdate: 'tool_call_update', toolCallId: 'edit', status: 'completed' }),
    })
    const { container } = render(() => (
      <PreferencesProvider>
        <MessageBubble message={message} premeasureMode host={{ messages: testMessageContext({ messages: () => [makeMessage({ id: 'edit-request', seq: 1n, agentProvider: AgentProvider.OPENCODE, spanId: 'edit', content: rawContent(request) })] }) }} />
      </PreferencesProvider>
    ))
    expect(container.textContent).toContain('hostAfter')
  })

  it('identifies request and result roles without arrival-order assumptions', () => {
    const plugin = providerFor(AgentProvider.OPENCODE)!
    expect(plugin?.transcript.spanRole?.(input({ sessionUpdate: 'tool_call', toolCallId: 'edit', status: 'pending' }))).toBe('opener')
    expect(plugin?.transcript.spanRole?.(input({ sessionUpdate: 'tool_call_update', toolCallId: 'edit', status: 'completed' }))).toBe('result')
  })
  it('renders every changed file in a completed call', () => {
    const tool = {
      sessionUpdate: 'tool_call_update',
      toolCallId: 'edit',
      kind: 'edit',
      status: 'completed',
      // The call states the file it asked to change. An edit that names none is not a
      // call this build draws (invariant I7); the RESULT is what states the second file.
      rawInput: { filePath: '/project/first.ts', oldString: 'firstBefore', newString: 'firstAfter' },
      content: [
        { type: 'diff', path: '/project/first.ts', oldText: 'firstBefore', newText: 'firstAfter' },
        { type: 'diff', path: '/project/second.ts', oldText: 'secondBefore', newText: 'secondAfter' },
      ],
    }
    const { container } = renderTool(tool, { premeasureMode: true })
    expect(container.textContent).toContain('firstAfter')
    expect(container.textContent).toContain('secondAfter')
    const meta = providerToolMeta(AgentProvider.OPENCODE, tool, { spanType: 'edit' })
    expect(meta?.hasDiff).toBe(true)
    expect(meta?.copyableContent()).toContain('secondAfter')
  })

  it('uses the linked request when a result omits the input and kind', () => {
    const request = {
      sessionUpdate: 'tool_call',
      toolCallId: 'edit',
      kind: 'edit',
      title: 'edit',
      rawInput: { filePath: '/project/file.ts', oldString: 'beforeRequest', newString: 'afterRequest' },
    }
    const { container } = renderTool({
      sessionUpdate: 'tool_call_update',
      toolCallId: 'edit',
      status: 'completed',
      content: [{ type: 'content', content: { type: 'text', text: 'Saved' } }],
    }, { premeasureMode: true, sources: testMessageSources({ request: () => (input(request)) }) })
    expect(container.textContent).toContain('afterRequest')
  })

  // A call that ended without applying anything draws NO diff. Nothing it asked for
  // reached the file, so the reason it gives is the whole answer -- the same rule
  // every provider now takes from `RequestedChangesBody`.
  it.each(['failed', 'cancelled'])('draws no requested diff after %s', (status) => {
    const { container } = renderTool({
      sessionUpdate: 'tool_call_update',
      toolCallId: 'edit',
      kind: 'edit',
      status,
      rawInput: { filePath: '/project/file.ts', oldString: 'attemptBefore', newString: 'attemptAfter' },
    })
    expect(container.textContent).not.toContain('Requested changes')
    expect(container.textContent).not.toContain('attemptAfter')
    expect(container.textContent).toContain(status === 'failed' ? 'Error' : 'Interrupted')
  })

  it('shows a command before the result arrives', () => {
    const { container } = renderTool({
      sessionUpdate: 'tool_call',
      toolCallId: 'command',
      kind: 'execute',
      status: 'pending',
      title: 'Run checks',
      rawInput: { command: 'npm test -- --runInBand' },
    }, { premeasureMode: true })
    expect(container.textContent).toContain('npm test -- --runInBand')
    expect(container.textContent).not.toContain('[no output]')
  })

  // A finished thought draws ONCE. With no result the renderer's request hook fires
  // and states the first line as a summary, and the row then drew that line again at
  // the head of the whole thought below it.
  it('draws the first line of a finished thought exactly once', () => {
    const { container } = renderTool({
      sessionUpdate: 'tool_call_update',
      toolCallId: 'thought',
      kind: 'think',
      status: 'completed',
      rawInput: {},
      content: [{ type: 'content', content: { type: 'text', text: 'Weighing two options.\nThe first one costs less.' } }],
    }, { premeasureMode: true })
    expect(container.textContent?.match(/Weighing two options\./g)).toHaveLength(1)
    expect(container.textContent).toContain('The first one costs less.')
  })

  // A thought still arriving has no answer yet, so the summary line IS the row.
  it('draws the first line of a running thought as its summary', () => {
    const { container } = renderTool({
      sessionUpdate: 'tool_call',
      toolCallId: 'thought',
      kind: 'think',
      status: 'in_progress',
      rawInput: { thought: 'Weighing two options.\nThe first one costs less.' },
    }, { premeasureMode: true })
    expect(container.textContent?.match(/Weighing two options\./g)).toHaveLength(1)
    expect(container.textContent).not.toContain('The first one costs less.')
  })
})
