import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { TRUNCATION_NOTICE } from '~/components/chat/truncationNotice'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { MESSAGE_SUPPLEMENT_FIELD } from '~/generated/contracts/worker-vocab'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { parseMessageContent } from '~/lib/messageParser'
import { makeMessage, rawContent } from '~/test-support/messageFactory'
import { testMessageSources } from '~/test-support/messageRenderSources'
import { pngBase64 } from '~/test-support/pngFixture'
import { providerRowImages, providerToolMeta } from '~/test-support/toolCallIr'
import { imageFromMessage } from '../../chatImageResolve'
import { MessageBubble } from '../../MessageBubble'
import { renderMessageContent } from '../../rowRenderers'
import { toolUseHeader } from '../../toolStyles.css'
import { providerFor, resolveMessageForRendering } from '../registry'
import { input } from '../testUtils'
import './plugin'
import '../testMocks'

function renderTool(toolName: string, args: Record<string, unknown>, result?: Record<string, unknown>, isError = false) {
  const start = { type: 'tool_execution_start', toolCallId: 'call', toolName, args }
  const payload = result ? { type: 'tool_execution_end', toolCallId: 'call', toolName, result, isError } : start
  const category = providerFor(AgentProvider.PI)!.transcript.classify(input(payload))
  return render(() => renderMessageContent(payload, {
    workingDir: '/project',
    premeasureMode: true,
    sources: testMessageSources({ current: () => input(payload), request: () => input(start) }),
  }, category, AgentProvider.PI))
}

const content = (text: string) => [{ type: 'text', text }]

describe('pi tool rendering', () => {
  // An end frame for a tool the plugin does not map, with no start row beside it. The
  // row carries the name Pi sent and the content Pi returned, and nothing the mapped
  // table would have supplied.
  it('shows an unmatched completion by its content alone', () => {
    const payload = { type: 'tool_execution_end', toolCallId: 'orphan', toolName: 'mystery_tool', result: { content: content('recovered output') }, isError: false }
    const category = providerFor(AgentProvider.PI)!.transcript.classify(input(payload))
    const { container } = render(() => renderMessageContent(payload, {
      premeasureMode: true,
      sources: testMessageSources({ current: () => input(payload), request: () => undefined }),
    }, category, AgentProvider.PI))
    expect(container.textContent).toContain('recovered output')
    for (const invented of ['Run command', 'Read file', 'Search'])
      expect(container.textContent).not.toContain(invented)
  })

  it('resolves stored MCP artifacts through the same path for the transcript and image viewer', () => {
    const data = pngBase64(12, 8)
    const path = '/tmp/pi-mcp-output-Ab123C/mcp-result-1234abcd.txt'
    const message = makeMessage({
      agentProvider: AgentProvider.PI,
      spanId: 'artifact',
      content: rawContent({ type: 'tool_execution_end', toolCallId: 'artifact', toolName: 'mcp', result: {
        content: [{ type: 'text', text: 'Short preview' }],
        details: { server: 'sample', tool: 'image', mcpResult: { omitted: true, fullResultPath: path } },
      } }),
      supplementalContent: rawContent({ provider: { toolCallId: 'artifact', toolName: 'mcp', mcpResultFile: { path, result: { content: [{ type: 'image', data, mimeType: 'image/png' }] } } } }),
    })
    const original = message.content.slice()
    const { container } = render(() => <PreferencesProvider><MessageBubble message={message} /></PreferencesProvider>)
    expect(container.querySelectorAll('img')).toHaveLength(1)
    expect(container.textContent).not.toContain('Short preview')
    expect(imageFromMessage(message, 0)?.data).toBe(data)
    expect(message.content).toEqual(original)
  })

  it('renders native MCP script failures through the shared error layout', () => {
    const { container } = renderTool('mcpScript', { code: 'throw new Error("probe")' }, {
      content: content('Error: MCP_SCRIPT_PROBE_FAILURE'),
      details: { mode: 'script', error: 'script_error', timeoutMs: 30000 },
    })
    expect(container.textContent).toContain('MCP_SCRIPT_PROBE_FAILURE')
    expect(container.querySelector('.lucide-circle-alert')).not.toBeNull()
  })

  it('renders a native resource image once and exposes it to the shared image viewer', () => {
    const data = 'iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAYAAABytg0kAAAAEUlEQVR4nGP4z8DwH4QZYAwAR8oH+WdZbrcAAAAASUVORK5CYII='
    const result = {
      content: [{ type: 'image', data, mimeType: 'image/png' }],
      details: { server: 'sample', resourceUri: 'probe://image', mcpResult: { contents: [{ uri: 'probe://image', blob: data, mimeType: 'image/png' }] } },
    }
    const { container } = renderTool('sample_read_resource', {}, result)
    expect(container.querySelectorAll('img')).toHaveLength(1)
    expect(container.textContent).not.toContain(data)
    const payload = { type: 'tool_execution_end', toolCallId: 'image', toolName: 'sample_read_resource', result: { ...result, content: [] } }
    const images = providerRowImages(AgentProvider.PI, payload, { spanType: 'sample_read_resource' })
    expect(images).toHaveLength(1)
    expect(images?.[0]?.data).toBe(data)
  })

  it('keeps the plan result concise when the request already contains the plan', () => {
    const plan = '# Welcome plan\n\nRead the sample.'
    const { container } = renderTool('plan_mode_complete', { plan }, { content: content(`**Proposed Plan**\n\n${plan}`), details: { plan } })
    expect(container.textContent).toContain('Plan ready for review')
    expect(container.textContent).not.toContain('Welcome plan')
  })

  // The closing row states the notice whether or not the paired request resolved.
  // The plan itself is on the REQUEST row, which carries it from the moment the call
  // opens -- see the case below. The reader this replaced consulted that pair, so the
  // same row drew a plan card before the pair landed and the notice afterwards.
  it('states the notice on the result row rather than the plan again', () => {
    const payload = { type: 'tool_execution_end', toolCallId: 'plan', toolName: 'plan_mode_complete', result: { content: content('Plan ready'), details: { plan: '# Recovered plan\n\nRead the sample.' } }, isError: false }
    const { container } = render(() => renderMessageContent(payload, { premeasureMode: true }, providerFor(AgentProvider.PI)!.transcript.classify(input(payload)), AgentProvider.PI))
    expect(container.textContent).toContain('Plan ready for review')
    expect(container.textContent).not.toContain('Recovered plan')
  })

  it('renders a completed plan request as Markdown in the transcript', () => {
    const payload = { type: 'tool_execution_start', toolCallId: 'plan', toolName: 'plan_mode_complete', args: { plan: '# Welcome plan\n\n- Keep **original bytes**.' } }
    const { container, getByRole } = render(() => renderMessageContent(payload, { premeasureMode: true }, providerFor(AgentProvider.PI)!.transcript.classify(input(payload)), AgentProvider.PI))
    expect(getByRole('heading', { name: 'Welcome plan' })).toBeInTheDocument()
    expect(container.querySelector('li strong')?.textContent).toBe('original bytes')
    expect(container.querySelector('.lucide-plane-takeoff')).not.toBeNull()
  })

  it('renders an image once when a todo result has an unknown shape', () => {
    const { container } = renderTool('todo', { action: 'list' }, {
      content: [{ type: 'image', mimeType: 'image/png', data: 'iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAYAAABytg0kAAAAEUlEQVR4nGP4z8DwH4QZYAwAR8oH+WdZbrcAAAAASUVORK5CYII=' }],
    })
    expect(container.querySelectorAll('img')).toHaveLength(1)
  })

  it('exposes native MCP images to the shared image viewer', () => {
    const payload = { type: 'tool_execution_end', toolCallId: 'mcp-image', toolName: 'mcp', result: {
      content: [],
      details: { mode: 'call', server: 'sample', tool: 'image', mcpResult: { content: [{ type: 'image', mimeType: 'image/png', data: 'image-bytes' }] } },
    } }
    const images = providerRowImages(AgentProvider.PI, payload, { spanType: 'mcp' })
    expect(images).toHaveLength(1)
    expect(images?.[0]?.data).toBe('image-bytes')
  })

  it('renders the rpiv todo list through the shared checklist', () => {
    const { container } = renderTool('todo', { action: 'list' }, {
      content: content('[pending] #1 Inspect sample'),
      details: { action: 'list', params: { action: 'list' }, tasks: [{ id: 1, subject: 'Inspect sample', status: 'pending' }] },
    })
    expect(container.querySelector('[data-task-checkbox="pending"]')).not.toBeNull()
    expect(container.textContent).toContain('Inspect sample')
    expect(container.textContent).not.toContain('"tasks"')
  })

  it('shows rpiv todo failures even when Pi reports isError false', () => {
    const { container } = renderTool('todo', { action: 'update', id: 99, status: 'completed' }, {
      content: content('Error: #99 not found'),
      details: { action: 'update', params: { action: 'update', id: 99, status: 'completed' }, error: '#99 not found', tasks: [] },
    })
    expect(container.textContent).toContain('#99 not found')
    expect(container.querySelector('.lucide-circle-alert')).not.toBeNull()
  })

  it('renders a background completion from the native custom message', () => {
    const payload = { type: 'message_end', message: {
      role: 'custom',
      customType: 'subagent-notification',
      display: true,
      content: '<task-notification>\n<task-id>wf_probe</task-id>\n<result>- Full **report** &amp; details</result>\n</task-notification>',
      details: { id: 'wf_probe', description: 'Workflow probe', status: 'completed', resultPreview: '- Full…', toolUses: 0, durationMs: 1200 },
    } }
    const plugin = providerFor(AgentProvider.PI)!
    const category = plugin?.transcript.classify(input(payload))
    const { container } = render(() => renderMessageContent(payload, { premeasureMode: true }, category, AgentProvider.PI))
    expect(container.textContent).toContain('Agent "Workflow probe" completed')
    expect(container.querySelector('li strong')?.textContent).toBe('report')
    expect(container.textContent).toContain('& details')
    expect(container.textContent).not.toContain('Full…')
    expect(providerToolMeta(AgentProvider.PI, payload, { category })?.copyableContent()).toBe('- Full **report** & details')
  })

  it('renders a visible custom plan as Markdown', () => {
    const payload = { type: 'message_end', message: { role: 'custom', customType: 'proposed-plan', display: true, content: '## Proposed plan\n\n- Read **sample.ts**' } }
    const category = providerFor(AgentProvider.PI)!.transcript.classify(input(payload))
    const { container } = render(() => renderMessageContent(payload, { premeasureMode: true }, category, AgentProvider.PI))
    expect(container.querySelector('h2')?.textContent).toBe('Proposed plan')
    expect(container.querySelector('li strong')?.textContent).toBe('sample.ts')
  })

  it('renders a retrieved agent report with its native identity and status', () => {
    const { container } = renderTool('get_subagent_result', { agent_id: 'child-1' }, {
      content: content('Agent: child-1\nType: Explore | Status: completed | Tool uses: 1 | 301 token | Context: 1% | Duration: 9.5s\nDescription: Inspect sample\n\n- **Report**'),
    })
    expect(container.textContent).toContain('Agent "Inspect sample" completed')
    expect(container.querySelector('li strong')?.textContent).toBe('Report')
    expect(container.textContent).toMatch(/Agent ID:\s*child-1/)
    expect(container.textContent).not.toContain('Status: completed |')
  })

  it('renders steering instructions with the shared prompt component', () => {
    const { container } = renderTool('steer_subagent', { agent_id: 'child-1', message: 'Read **sample.ts**' })
    expect(container.querySelector(`.${toolUseHeader}`)?.textContent).toContain('child-1')
    expect(container.querySelector('strong')?.textContent).toBe('sample.ts')
    expect(container.textContent).not.toContain('"message"')
  })

  it('shows a rejected steering request as a failure without isError', () => {
    const { container } = renderTool('steer_subagent', { agent_id: 'child-1', message: 'Read sample' }, {
      content: content('Agent "child-1" is not running (status: completed). Cannot steer a non-running agent.'),
    })
    expect(container.querySelectorAll('.lucide-circle-alert')).toHaveLength(1)
    expect(container.textContent).toContain('Cannot steer a non-running agent.')
  })

  it.each(['constructor', 'toString', '__proto__'])('renders an extension called %s through the generic fallback', (toolName) => {
    const request = renderTool(toolName, { query: 'marker' })
    expect(request.container.textContent).toContain(toolName)
    request.unmount()
    const result = renderTool(toolName, {}, { content: content('Extension report') })
    expect(result.container.textContent).toContain('Extension report')
  })

  it('renders an agent launch through the shared prompt component', () => {
    const { container } = renderTool('Agent', { description: 'Inspect sample', subagent_type: 'Explore', prompt: '- Read **sample.ts**' })
    expect(container.querySelector(`.${toolUseHeader}`)?.textContent).toBe('Inspect sample (Explore)')
    expect(container.querySelector('li strong')?.textContent).toBe('sample.ts')
    expect(container.textContent).not.toContain('"prompt"')
  })

  it('renders an agent report and native usage through the shared result component', () => {
    const { container } = renderTool('Agent', { description: 'Inspect sample', prompt: 'Read sample.ts' }, {
      content: content('Agent completed in 1.2s (1 tool uses, 100 token).\n\n- Read **sample.ts**'),
      details: { description: 'Inspect sample', subagentType: 'Explore', status: 'completed', agentId: 'child-1', toolUses: 1, tokens: '100 token', durationMs: 1200 },
    })
    expect(container.textContent).toContain('Agent "Inspect sample" completed')
    expect(container.querySelector('li strong')?.textContent).toBe('sample.ts')
    expect(container.textContent).toMatch(/Agent ID:\s*child-1/)
    expect(container.textContent).toMatch(/Tool uses:\s*1/)
    expect(container.textContent).not.toContain('Agent completed in')
    expect(container.textContent).not.toContain('"durationMs"')
  })

  it('uses native agent errors even when the extension returns isError false', () => {
    const { container } = renderTool('Agent', { description: 'Inspect sample' }, {
      content: content('Agent failed: Source unavailable\n\n- Partial **finding**'),
      details: { description: 'Inspect sample', status: 'error', error: 'Source unavailable', agentId: 'child-1' },
    })
    expect(container.textContent).toContain('Agent "Inspect sample" failed')
    expect(container.querySelector('li strong')?.textContent).toBe('finding')
    expect(container.querySelectorAll('.lucide-circle-alert')).toHaveLength(1)
  })

  it('keeps a completed background launch marked as running', () => {
    const { container } = renderTool('Agent', { description: 'Inspect sample', run_in_background: true }, {
      content: content('Agent started in background.\nAgent ID: child-1\nType: Explore\nDescription: Inspect sample'),
      details: { status: 'background', agentId: 'child-1', description: 'Inspect sample', toolUses: 0, durationMs: 0 },
    })
    expect(container.textContent).toContain('Agent "Inspect sample" running')
    expect(container.textContent).toMatch(/Tool uses:\s*0/)
    expect(container.querySelector('.lucide-check')).toBeNull()
  })

  it('copies only the report after it removes a verified native summary', () => {
    const payload = { type: 'tool_execution_end', toolCallId: 'call', toolName: 'Agent', result: {
      content: content('Agent completed in 1.2s (1 tool uses).\n\n- **Report**'),
      details: { status: 'completed', toolUses: 1, durationMs: 1200 },
    } }
    const meta = providerToolMeta(AgentProvider.PI, payload, { category: { kind: 'tool_result' }, spanType: 'Agent' })
    expect(meta?.copyableContent?.()).toBe('- **Report**')
  })

  it('uses the common glob title for find', () => {
    const { container } = renderTool('find', { pattern: '*.ts', path: '/project/src' })
    expect(container.querySelector(`.${toolUseHeader}`)?.textContent).toBe('*.ts src')
  })

  it('renders directory output with the shared entry summary', () => {
    const { container } = renderTool('ls', { path: '/project' }, { content: [{ type: 'text', text: 'a.ts\nsrc/' }] })
    expect(container.textContent).toContain('2 entries')
    expect(container.textContent).toContain('src/')
    expect(container.textContent).not.toContain('2 files')
  })

  it('resolves an image file path from the native read request', () => {
    const request = input({ type: 'tool_execution_start', toolCallId: 'call', toolName: 'read', args: { path: '/project/image.png' } })
    const payload = { type: 'tool_execution_end', toolCallId: 'call', toolName: 'read', result: { content: [{ type: 'image', mimeType: 'image/png', data: 'image-bytes' }] } }
    const images = providerRowImages(AgentProvider.PI, payload, { spanType: 'read', request })
    expect(images?.[0]?.filePath).toBe('/project/image.png')
  })

  it('renders extension result resources and structured details', () => {
    const { container } = renderTool('extension_lookup', { query: 'marker' }, {
      content: [{ type: 'resource', resource: { uri: 'probe://item', text: 'Extension resource' } }],
      details: { count: 0 },
    })
    expect(container.textContent).toContain('Extension resource')
    expect(container.textContent).toMatch(/"count"\s*:\s*0/)
  })

  it('preserves resources and details when an extension call fails', () => {
    const { container } = renderTool('extension_lookup', {}, {
      content: [{ type: 'resource', resource: { uri: 'probe://failure', text: 'Failure resource' } }],
      details: { reason: 'source unavailable' },
    }, true)
    expect(container.textContent).toContain('Failure resource')
    expect(container.textContent).toContain('source unavailable')
    expect(container.textContent).toMatch(/failed|error/i)
  })

  it('offers expansion for a long extension result', () => {
    const text = 'first\nsecond\nthird\nfourth'
    const payload = { type: 'tool_execution_end', toolCallId: 'call', toolName: 'extension_lookup', result: { content: content(text) } }
    const meta = providerToolMeta(AgentProvider.PI, payload, { category: { kind: 'tool_result' }, spanType: 'extension_lookup' })
    expect(meta).toMatchObject({ collapsible: true, hasCopyable: true })
    expect(meta?.copyableContent()).toBe(text)
  })

  it('copies the displayed raw diff when a diff cannot be parsed', () => {
    const diff = 'A provider diff in an unknown format'
    const payload = { type: 'tool_execution_end', toolCallId: 'call', toolName: 'edit', result: { content: content('Edit completed'), details: { diff } } }
    const meta = providerToolMeta(AgentProvider.PI, payload, { category: { kind: 'tool_result' }, spanType: 'edit' })
    expect(meta?.hasDiff).toBe(false)
    expect(meta?.copyableContent()).toBe(diff)
  })

  it('offers expansion and copying for a long search result', () => {
    const text = 'a.ts\nb.ts\nc.ts\nd.ts'
    const payload = { type: 'tool_execution_end', toolCallId: 'call', toolName: 'find', result: { content: content(text) }, isError: false }
    const meta = providerToolMeta(AgentProvider.PI, payload, { category: { kind: 'tool_result' }, spanType: 'find' })
    expect(meta).toMatchObject({ collapsible: true, hasCopyable: true })
    expect(meta?.copyableContent()).toBe(text)
  })

  it('offers expansion for a long failed edit report', () => {
    const text = 'failure\nfirst detail\nsecond detail\nthird detail'
    const payload = { type: 'tool_execution_end', toolCallId: 'call', toolName: 'edit', result: { content: content(text) }, isError: true }
    const meta = providerToolMeta(AgentProvider.PI, payload, { category: { kind: 'tool_result' }, spanType: 'edit' })
    expect(meta?.collapsible).toBe(true)
    expect(meta?.hasDiff).toBe(false)
  })

  it('uses the common relative path and line range for a read request', () => {
    const { container } = renderTool('read', { path: '/project/src/file.ts', offset: 10, limit: 2 })
    expect(container.textContent).toContain('src/file.ts')
    expect(container.textContent).toContain('(Line 10–11)')
    expect(container.textContent).not.toContain('/project/')
  })

  it('does not report zero edits for a singleton edit request', () => {
    const { container } = renderTool('edit', { path: '/project/file.ts', oldText: 'before', newText: 'after' })
    expect(container.querySelector(`.${toolUseHeader}`)?.textContent).toContain('file.ts')
    expect(container.textContent).not.toContain('0 edit')
    expect(container.textContent).not.toContain('/project/')
  })

  it('renders a PowerShell execution with the shared command status', () => {
    const { container } = renderTool('powershell', { command: 'Write-Output marker; exit 7' }, { content: content('marker\n\nCommand exited with code 7') }, true)
    expect(container.textContent).toContain('marker')
    expect(container.textContent).toContain('Error (exit 7)')
    expect(container.textContent).not.toContain('Command exited with code')
  })

  // One notice, whichever path shows the cut. A truncation census read two spellings
  // for one state on the same operation: a native search said "Output truncated" and a
  // shell command said "[output truncated]" (RL-020).
  it('retains the command truncation flag and states it in the shared words', () => {
    const { container } = renderTool('bash', { command: 'output-command' }, { content: content('partial output'), details: { truncation: { truncated: true }, fullOutputPath: '/project/output.log' } })
    expect(container.textContent).toContain(TRUNCATION_NOTICE)
    expect(container.textContent).not.toContain('[output truncated]')
  })

  it('shows a failed read as an error instead of file contents', () => {
    const { container } = renderTool('read', { path: '/project/missing.ts' }, { content: content('ENOENT: file does not exist') }, true)
    expect(container.textContent).toContain('ENOENT')
    expect(container.textContent).toMatch(/Error|Failed/)
  })

  it('renders file search results with the shared count summary', () => {
    const { container } = renderTool('find', { path: '/project', pattern: '*.ts' }, { content: content('first.ts\nsecond.ts') })
    expect(container.textContent).toContain('Found 2 files')
    expect(container.textContent).toContain('first.ts')
    expect(container.textContent).toContain('second.ts')
  })

  it('renders grep matches with the shared count summary', () => {
    const { container } = renderTool('grep', { path: '/project', pattern: 'answer' }, { content: content('first.ts:3: answer = 42') })
    expect(container.textContent).toContain('1 match')
    expect(container.textContent).toContain('first.ts:3: answer = 42')
  })
})

/**
 * The output the worker broadcasts while a call runs.
 *
 * `ToolMessage` draws it only while the row is `in_progress` AND its result is absent,
 * so a result attached to a running row hides it. Every tool `PI_TOOL_KINDS` does not
 * hold reaches the `mcp` entry, so that entry decides this for each Pi extension and
 * each Model Context Protocol bridge.
 */
describe('a pi tool row that has not returned', () => {
  function renderRunning(toolName: string, args: Record<string, unknown>, outputTail: string) {
    const payload = { type: 'tool_execution_start', toolCallId: 'call', toolName, args }
    const category = providerFor(AgentProvider.PI)!.transcript.classify(input(payload))
    return render(() => renderMessageContent(payload, {
      premeasureMode: true,
      sources: testMessageSources({ current: () => input(payload) }),
      toolProgress: { liveTail: () => ({ outputTail }) },
    }, category, AgentProvider.PI))
  }

  it('draws the streaming output of an unrecognized extension', () => {
    const { container } = renderRunning('extension_lookup', { query: 'marker' }, 'reading the index')
    expect(container.textContent).toContain('reading the index')
  })

  it('draws the streaming output of a to-do call whose checklist it cannot read', () => {
    const { container } = renderRunning('todo', { action: 'teleport' }, 'resolving the action')
    expect(container.textContent).toContain('resolving the action')
  })
})

describe('pi rows on the shared tool path', () => {
  // The open request draws the list the call is about to save. The row used to draw
  // its header alone, so the reader waited for the result to see what changed.
  it('draws the checklist of an open to-do request', () => {
    const { container } = renderTool('todo', { action: 'list' })
    expect(container.textContent).toContain('To-do list')
  })

  it('uses the shared grep icon and states the path below the pattern', () => {
    const { container } = renderTool('grep', { pattern: 'answer', path: '/project/src' })
    expect(container.querySelector('.lucide-text-search')).not.toBeNull()
    expect(container.textContent).toContain('"answer"')
    expect(container.textContent).toContain('src')
  })

  // A result with no request beside it states WHICH tool it belongs to.
  it('draws its own header when no request row sits beside the result', () => {
    const payload = { type: 'tool_execution_end', toolCallId: 'call', toolName: 'read', result: { content: content('first line') } }
    const category = providerFor(AgentProvider.PI)!.transcript.classify(input(payload))
    const { container } = render(() => renderMessageContent(payload, {
      premeasureMode: true,
      sources: testMessageSources({ current: () => input(payload) }),
    }, category, AgentProvider.PI))
    expect(container.textContent).toContain('Read')
    expect(container.querySelector('.lucide-eye')).not.toBeNull()
  })
})

describe('a pi tool row the turn ended before the call did', () => {
  // The worker stores the agent's own tool_execution_start frame and keeps the last
  // partial result beside it. Nothing in the row's bytes claims a failure.
  const start = {
    type: 'tool_execution_start',
    toolCallId: 'call',
    toolName: 'bash',
    args: { command: 'bun test --coverage' },
  }
  const message = makeMessage({
    agentProvider: AgentProvider.PI,
    completion: MessageCompletion.INTERRUPTED,
    content: rawContent(start),
    supplementalContent: rawContent({
      [MESSAGE_SUPPLEMENT_FIELD.Provider]: {
        toolCallId: 'call',
        toolName: 'bash',
        partialResult: { content: content('Running 240 tests'), details: {} },
      },
    }),
  })
  const plugin = () => providerFor(AgentProvider.PI)!

  it('reads the retained start frame as the call result', () => {
    expect(plugin().transcript.spanRole!(resolveMessageForRendering(parseMessageContent(message), AgentProvider.PI))).toBe('result')
    // The same frame with no completion is the call's REQUEST, which is the row the
    // agent sent when the call began. A COPY first -- `parseMessageContent` caches by
    // message, and a `delete` on the cached object would reach every later reader of
    // this same message. The key is deleted rather than stated `undefined`, which the
    // exact-optional rule reads as a different object.
    const uncompleted = { ...parseMessageContent(message) }
    delete uncompleted.completion
    expect(plugin().transcript.spanRole!(resolveMessageForRendering(uncompleted, AgentProvider.PI))).toBe('request')
  })

  it('shows the partial output under one Interrupted header', () => {
    const parsed = resolveMessageForRendering(parseMessageContent(message), AgentProvider.PI)
    const category = plugin().transcript.classify({ ...parsed, agentProvider: AgentProvider.PI })
    const { container } = render(() => renderMessageContent(
      parsed.parentObject,
      { premeasureMode: true, sources: testMessageSources({ current: () => parsed }) },
      category,
      AgentProvider.PI,
      message.completion,
    ))
    expect(container.textContent).toContain('Running 240 tests')
    expect(container.textContent).toContain('Interrupted')
    expect(container.textContent).not.toContain('[no output]')
  })
})
