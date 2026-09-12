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

  // Cursor writes a plan with a `createPlan` call whose stored content carries the tool
  // name alone. The plan body arrives as supplemental content the worker recovered from
  // the approval request, and a full plan belongs in the transcript -- Claude, Codex and
  // ZCode all render theirs there. See RL-003.
  describe('a plan', () => {
    const PLAN = '# Add CHANGELOG.md\n\nPLANMARKER-7\n\n## Steps\n\n- Seed an Unreleased section.'
    const call = { sessionUpdate: 'tool_call', status: 'pending', title: 'Create Plan', kind: 'other', toolCallId: 'plan-1', rawInput: { _toolName: 'createPlan' } }
    const done = { sessionUpdate: 'tool_call_update', status: 'completed', title: 'Create Plan', kind: 'other', toolCallId: 'plan-1', rawInput: { _toolName: 'createPlan' } }

    function parse(tool: Record<string, unknown>) {
      return {
        ...input(tool),
        supplementalContent: {
          sessionUpdate: tool.sessionUpdate,
          status: tool.status,
          toolCallId: tool.toolCallId,
          rawInput: { _toolName: 'createPlan', name: 'Add CHANGELOG.md', plan: PLAN },
        },
      }
    }

    function renderRow(tool: Record<string, unknown>, role: 'opener' | 'result', result?: Record<string, unknown>) {
      const parsed = parse(tool)
      const category = providerFor(AgentProvider.CURSOR)!.classify(parsed)
      const sources = testMessageSources({
        current: () => parsed,
        role: () => role,
        ...(result ? { result: () => parse(result) } : {}),
      })
      return render(() => renderMessageContent(tool, { premeasureMode: true, sources }, category, AgentProvider.CURSOR))
    }

    // While the approval is open there is no completing row, so the proposing row is where
    // the reader has to be able to read the plan.
    it('renders the recovered plan while the call is still open', () => {
      const { container } = renderRow(call, 'opener')
      expect(container.textContent).toContain('PLANMARKER-7')
      expect(container.textContent).toContain('Seed an Unreleased section.')
    })

    it('names the row by the plan it writes', () => {
      expect(renderRow(call, 'opener').container.textContent).toContain('Add CHANGELOG.md')
    })

    // Once the call completes, the completing row draws the plan and the proposing row
    // must not draw it as well.
    it('renders the plan on the row that completes the call', () => {
      expect(renderRow(done, 'result').container.textContent).toContain('PLANMARKER-7')
    })

    it('does not repeat the plan on the proposing row once a result exists', () => {
      const { container } = renderRow(call, 'opener', done)
      expect(container.textContent).not.toContain('PLANMARKER-7')
    })

    // The whole plan as JSON is what a reader saw where the plan belonged.
    it.each([['opener', call], ['result', done]] as const)('states no raw arguments on the %s row', (role, tool) => {
      expect(renderRow(tool, role).container.textContent).not.toContain('"_toolName"')
    })
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

// Cursor reports a REFUSED read as a successful call: its content is the empty string
// and `exceededLimit` is set, while its own sentence about why lives in the stored
// tool result. Dropping that sentence made the row state that a 4.8 MB file was
// empty. See RL-037.
describe('cursor read that returned no content', () => {
  const storedRead = (result: string, success: Record<string, unknown>) => ({
    rawOutput: {
      content: [{ type: 'tool-result', toolCallId: 'cursor-tool', toolName: 'Read', result }],
      providerOptions: { cursor: { highLevelToolCallResult: { isError: false, output: { success } } } },
    },
  })
  const refusal = 'File content (5040000 characters) exceeds maximum allowed characters (100000 characters).'

  it('states why the read returned nothing', () => {
    const { container } = renderTool(
      { kind: 'read', rawInput: { path: 'bigfile.txt' }, rawOutput: { content: '' } },
      storedRead(refusal, { content: '', exceededLimit: true, totalLines: 60001, fileSize: 5040000, path: 'bigfile.txt' }),
    )
    expect(container.textContent).toContain('exceeds maximum allowed characters')
    expect(container.textContent).not.toContain('[no output]')
  })

  it('still renders a file the read did return', () => {
    const { container } = renderTool(
      { kind: 'read', rawInput: { path: 'small.txt' }, rawOutput: { content: '' } },
      storedRead('alpha\nbeta', { content: 'alpha\nbeta', totalLines: 2, path: 'small.txt' }),
    )
    expect(container.textContent).toContain('alpha')
    expect(container.textContent).not.toContain('[no output]')
  })

  // Nothing returned and nothing said is the one case the shared notice is for.
  it('says only that nothing came back when the provider gave no reason', () => {
    const { container } = renderTool(
      { kind: 'read', rawInput: { path: 'empty.txt' }, rawOutput: { content: '' } },
      storedRead('', { content: '', totalLines: 0, path: 'empty.txt' }),
    )
    expect(container.textContent).toContain('[no output]')
  })
})
