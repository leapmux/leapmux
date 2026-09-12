import { render, waitFor } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { testMessageContext } from '~/test-support/messageContext'
import { makeMessage, rawContent } from '~/test-support/messageFactory'
import { testMessageSources } from '~/test-support/messageRenderSources'
import { pngBase64 } from '~/test-support/pngFixture'
import { ChatImageViewer } from '../../ChatImageViewer'
import { renderMessageContent } from '../../messageRenderers'
import { providerFor } from '../registry'
import { input, toolMessageInput } from '../testUtils'
import './plugin'
import '../testMocks'

function renderTool(fields: Record<string, unknown>, supplemental?: Record<string, unknown>) {
  const tool = { sessionUpdate: 'tool_call_update', status: 'completed', toolCallId: 'cursor-tool', ...fields }
  const parsed = { ...input(tool), supplementalContent: supplemental ? { sessionUpdate: tool.sessionUpdate, status: tool.status, toolCallId: tool.toolCallId, ...supplemental } : undefined }
  const category = providerFor(AgentProvider.CURSOR)!.classify(parsed)
  return render(() => renderMessageContent(tool, { premeasureMode: true, sources: testMessageSources({ current: () => parsed }) }, category, AgentProvider.CURSOR))
}

describe('cursor native tool rendering', () => {
  it('keeps a recovered final report out of the request preview', () => {
    const { container } = renderTool({ sessionUpdate: 'tool_call', status: 'pending', kind: 'other', rawInput: { _toolName: 'task', description: 'Inspect sample', prompt: 'Read the file' } }, {
      rawOutput: {
        content: [{ type: 'tool-result', toolCallId: 'cursor-tool', toolName: 'Task', result: 'Native final report' }],
        providerOptions: { cursor: { highLevelToolCallResult: { output: { success: { conversationSteps: [{ assistantMessage: { text: 'Native final report' } }] } } } } },
      },
    })
    expect(container.textContent).toContain('Read the file')
    expect(container.textContent).not.toContain('Native final report')
  })

  it('uses the shared agent prompt for a task launch', () => {
    const { container } = renderTool({ sessionUpdate: 'tool_call', status: 'pending', title: 'Task: Inspect sample', kind: 'other', rawInput: {
      _toolName: 'task',
      description: 'Inspect sample',
      prompt: '- Read **sample.py**',
      subagentType: { explore: {} },
    } })
    expect(container.textContent).toContain('Inspect sample (Explore)')
    expect(container.querySelector('li strong')?.textContent).toBe('sample.py')
    expect(container.textContent).not.toContain('"_toolName"')
  })

  it('renders the native subagent report that ACP omits', () => {
    const { container } = renderTool({ kind: 'other', rawInput: { _toolName: 'task', description: 'Inspect sample' }, rawOutput: { durationMs: 1000, isBackground: false } }, {
      rawOutput: {
        content: [{ type: 'tool-result', toolCallId: 'cursor-tool', toolName: 'Task', result: 'Native model wrapper' }],
        providerOptions: { cursor: { highLevelToolCallResult: { output: { success: {
          agentId: 'child-1',
          durationMs: '1000',
          conversationSteps: [{ assistantMessage: { text: '- Read **sample.py**' } }],
        } } } } },
      },
    })
    expect(container.textContent).toContain('Agent "Inspect sample" completed')
    expect(container.querySelector('li strong')?.textContent).toBe('sample.py')
    expect(container.textContent).toMatch(/Agent ID:\s*child-1/)
    expect(container.textContent).toMatch(/Duration:\s*1.0s/)
    expect(container.textContent).not.toContain('Native model wrapper')
  })

  it('shows a missing report explicitly when the native store is absent', () => {
    const { container } = renderTool({ kind: 'other', rawInput: { _toolName: 'task', description: 'Inspect sample' }, rawOutput: { durationMs: 1000, isBackground: false } })
    expect(container.textContent).toContain('Agent "Inspect sample" completed')
    expect(container.textContent).toContain('The provider did not supply a report.')
  })

  it('keeps a background launch marked as running', () => {
    const { container } = renderTool({ kind: 'other', rawInput: { _toolName: 'task', description: 'Inspect sample' }, rawOutput: { durationMs: 0, isBackground: true } })
    expect(container.textContent).toContain('Agent "Inspect sample" running')
    expect(container.querySelector('.lucide-check')).toBeNull()
    expect(container.textContent).not.toContain('did not supply a report')
  })

  it('shows one failure status when a subagent launch fails', () => {
    const { container } = renderTool({ status: 'failed', kind: 'other', rawInput: { _toolName: 'task', description: 'Inspect sample' }, rawOutput: { error: 'Source unavailable' } })
    expect(container.querySelectorAll('.lucide-circle-alert')).toHaveLength(1)
    expect(container.textContent).toContain('Agent "Inspect sample" failed')
    expect(container.textContent).toContain('Source unavailable')
  })

  it.each([{ scenario: 'absent', experimental_content: undefined }, { scenario: 'empty', experimental_content: [] }])('uses native image content when the usual content array is $scenario', ({ experimental_content }) => {
    const data = pngBase64(12, 8)
    const fields = { kind: 'other', rawInput: { providerIdentifier: 'probe', toolName: 'image' } }
    const rawOutput = {
      content: [{ type: 'tool-result', toolCallId: 'cursor-tool', toolName: 'mcp_probe_image', result: 'Image returned', experimental_content, providerOptions: { cursor: { imageDescriptions: { 1: 'A red square' } } } }],
      providerOptions: { cursor: { highLevelToolCallResult: { output: { success: { content: [{ text: { text: 'Native **caption**' } }, { image: { data, mimeType: 'image/png' } }] } } } } },
    }
    const { container } = renderTool(fields, { rawOutput })
    expect(container.querySelector('img')?.getAttribute('src')).toContain(data)
    expect(container.querySelector('img')?.getAttribute('alt')).toBe('A red square')
    expect(container.textContent).toContain('A red square')
    expect(container.querySelector('strong')?.textContent).toBe('caption')
    const row = toolMessageInput({ sessionUpdate: 'tool_call_update', toolCallId: 'cursor-tool', status: 'completed', ...fields })
    row.parsed.supplementalContent = { sessionUpdate: 'tool_call_update', toolCallId: 'cursor-tool', status: 'completed', rawOutput }
    expect(providerFor(AgentProvider.CURSOR)!.toolResultImages?.(row)?.map(image => image.data)).toEqual([data])
    expect(providerFor(AgentProvider.CURSOR)!.toolResultMeta?.({ kind: 'tool_use', toolUse: {}, toolName: 'image', content: [] }, row)?.copyableContent()).toContain('A red square')
  })

  it('opens the native image with its description as alternative text', async () => {
    const tool = { sessionUpdate: 'tool_call_update', toolCallId: 'image', status: 'completed', kind: 'other' }
    const message = makeMessage({ agentProvider: AgentProvider.CURSOR, seq: 7n, spanId: 'image', content: rawContent(tool), supplementalContent: rawContent({ provider: {
      ...tool,
      rawOutput: {
        content: [{ type: 'tool-result', toolCallId: 'image', toolName: 'mcp_probe_image', experimental_content: [{ type: 'image', mimeType: 'image/png', data: pngBase64(12, 8) }], providerOptions: { cursor: { imageDescriptions: { 0: 'A red square' } } } }],
      },
    } }) })
    const context = testMessageContext({ messages: () => [message] })
    const { container } = render(() => <ChatImageViewer workerId="worker" agentId="agent" seq={7n} imageIndex={0} title="Screenshot" messages={context} />)
    await waitFor(() => expect(container.querySelector('img')?.getAttribute('alt')).toBe('A red square'))
  })

  it('renders recovered search matches from the saved tool result', () => {
    const { container } = renderTool({
      kind: 'search',
      rawInput: { pattern: 'answer', path: '/project' },
      rawOutput: { totalMatches: 1 },
    }, {
      rawOutput: {
        content: [{ type: 'tool-result', toolCallId: 'cursor-tool', toolName: 'Grep', result: 'sample.py:7:answer = 42' }],
        providerOptions: { cursor: { highLevelToolCallResult: { output: { success: {
          workspaceResults: { '/project': { content: { matches: [{ file: './sample.py', matches: [{ lineNumber: 7, content: 'answer = 42' }] }], totalMatchedLines: 1 } } },
        } } } } },
      },
    })
    expect(container.textContent).toContain('answer = 42')
    expect(container.textContent).toContain('sample.py')
    expect(container.textContent).toContain('7')
  })

  it('uses the complete saved file change instead of the protocol fragment', () => {
    const { container } = renderTool({
      kind: 'edit',
      rawInput: { path: '/project/sample.py', old_string: '41', new_string: '42' },
      content: [{ type: 'diff', path: '/project/sample.py', oldText: '41', newText: '42' }],
    }, {
      rawOutput: {
        content: [{ type: 'tool-result', toolCallId: 'cursor-tool', toolName: 'StrReplace', result: 'Saved' }],
        providerOptions: { cursor: { highLevelToolCallResult: { output: { success: { path: '/project/sample.py', beforeFullFileContent: 'answer = 41\n', afterFullFileContent: 'answer = 42\n' } } } } },
      },
    })
    expect(container.textContent).toContain('answer = 41')
    expect(container.textContent).toContain('answer = 42')
  })

  it('ignores a saved result from a different tool call', () => {
    const { container } = renderTool({
      kind: 'search',
      rawInput: { pattern: 'answer' },
      rawOutput: { totalMatches: 0 },
    }, {
      rawOutput: { content: [{ type: 'tool-result', toolCallId: 'other-tool', toolName: 'Grep', result: 'unrelated-result' }] },
    })
    expect(container.textContent).not.toContain('unrelated-result')
    expect(container.textContent).toContain('No matches found')
  })

  it('shows stdout, stderr, and the command exit code', () => {
    const { container } = renderTool({
      kind: 'execute',
      rawInput: { command: 'python3 sample.py' },
      rawOutput: { stdout: 'answer = 42\n', stderr: 'syntax error\n', exitCode: 2 },
    })
    expect(container.textContent).toContain('answer = 42')
    expect(container.textContent).toContain('syntax error')
    expect(container.textContent).toContain('Error (exit 2)')
  })

  it('renders plain file content from rawOutput with line numbers', () => {
    const { container } = renderTool({
      kind: 'read',
      rawInput: { path: '/project/sample.py' },
      rawOutput: { content: 'answer = 42\nprint(answer)' },
      locations: [{ path: '/project/sample.py', line: 7 }],
    })
    expect(container.textContent).toContain('answer = 42')
    expect(container.textContent).toContain('7')
    expect(container.textContent).toContain('8')
  })

  it('shows an explicit zero-match result', () => {
    const { container } = renderTool({
      kind: 'search',
      rawInput: { pattern: 'missing' },
      rawOutput: { totalMatches: 0, truncated: false },
    })
    expect(container.textContent).toContain('No matches found')
  })

  it('removes diff headers that Cursor includes in a new file', () => {
    const { container } = renderTool({
      kind: 'edit',
      content: [{ type: 'diff', path: '/project/new.py', oldText: '-- /dev/null', newText: '++ b//project/new.py\nanswer = 42' }],
    })
    expect(container.textContent).toContain('answer = 42')
    expect(container.textContent).not.toContain('/dev/null')
    expect(container.textContent).not.toContain('++ b/')
  })
})
