import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { testMessageSources } from '~/test-support/messageRenderSources'
import { diffAdded } from '../../diff/diffStyles.css'
import { renderMessageContent } from '../../messageRenderers'
import { COLLAPSED_RESULT_ROWS } from '../../results/collapse'
import { toolInputPath } from '../../toolStyles.css'
import { parsedMessageForRendering, providerFor } from '../registry'
import { input, toolMessageInput } from '../testUtils'
import './index'
import '../testMocks'

const provider = () => providerFor(AgentProvider.ZCODE)!
const event = (kind: string, fields: Record<string, unknown>) => ({ type: 'tool.updated', payload: { kind, toolCallId: 'call', ...fields } })

function renderRequest(toolName: string, args: Record<string, unknown>) {
  const request = event('scheduled', { toolName, input: args })
  return render(() => renderMessageContent(request, { workingDir: '/project', premeasureMode: true }, provider().classify(input(request)), AgentProvider.ZCODE))
}

it('renders the ZCode plan in the transcript through the shared plan layout', () => {
  const { container, getByRole } = renderRequest('ExitPlanMode', { plan: '# Welcome plan\n\n- Keep **original bytes**.' })
  expect(getByRole('heading', { name: 'Welcome plan' })).toBeInTheDocument()
  expect(container.querySelector('li strong')?.textContent).toBe('original bytes')
  expect(container.querySelector('.lucide-plane-takeoff')).not.toBeNull()
})

it('renders a plan preserved from a native control request', () => {
  const request = { id: 'server-1', method: 'interaction/requestUserInput', params: { requestId: 'plan', toolName: 'ExitPlanMode', schema: { interaction: 'plan_approval' }, input: { plan: '# Stored plan\n\n- Keep **original bytes**.' } } }
  const { getByRole, container } = render(() => renderMessageContent(request, { premeasureMode: true }, provider().classify(input(request)), AgentProvider.ZCODE))
  expect(getByRole('heading', { name: 'Stored plan' })).toBeInTheDocument()
  expect(container.querySelector('li strong')?.textContent).toBe('original bytes')
  expect(container.querySelector('.lucide-plane-takeoff')).not.toBeNull()
})

function renderResult(toolName: string, args: Record<string, unknown>, result: Record<string, unknown>, expanded = false) {
  const request = input(event('scheduled', { toolName, input: args }))
  const end = event('result', { result })
  const row = toolMessageInput(end, toolName, request)
  const category = provider().classify({ ...row.parsed, spanType: toolName })
  const view = render(() => renderMessageContent(end, {
    workingDir: '/project',
    spanType: toolName,
    premeasureMode: true,
    getMessageUiState: () => expanded,
    sources: testMessageSources({ current: () => row.parsed, request: () => request }),
  }, category, AgentProvider.ZCODE))
  return { ...view, category, meta: provider().toolResultMeta?.(category, row) }
}

describe('zcode native tool rendering', () => {
  it('uses the shared subagent title', () => {
    const { container } = renderRequest('Agent', { description: 'Inspect project structure', subagent_type: 'explore', prompt: 'Read the entry points.' })
    expect(container.textContent).toContain('Inspect project structure (explore)')
  })

  it('renders the subagent report and separates its native usage footer', () => {
    const report = '**Findings**\n\nThe entry points are present.'
    const content = `${report}\nagentId: agent_child (use SendMessage with to: 'agent_child' to continue this agent)\n<usage>subagent_tokens: 123\ntool_uses: 2\nduration_ms: 1500</usage>`
    const { container, meta } = renderResult('Agent', { description: 'Inspect project structure' }, { success: true, content })
    expect(container.textContent).toContain('Agent "Inspect project structure" completed')
    expect(container.textContent).toContain('Agent ID:agent_child')
    expect(container.querySelector('strong')?.textContent).toBe('Findings')
    expect(container.textContent).not.toContain('use SendMessage')
    expect(meta?.copyableContent()).toBe(report)
  })

  it('resolves streamed arguments from supplemental content without changing the provider request', () => {
    const request = event('scheduled', { toolName: 'Bash', inputOmitted: true, inputRef: 'model_stream' })
    const original = JSON.stringify(request)
    const parsed = { ...input(request), supplementalContent: event('scheduled', { input: { command: 'printf recovered' } }) }
    const resolved = parsedMessageForRendering(parsed, AgentProvider.ZCODE)
    const { container } = render(() => renderMessageContent(resolved.parentObject, { premeasureMode: true }, provider().classify(resolved), AgentProvider.ZCODE))
    expect(container.textContent).toContain('printf recovered')
    expect(JSON.stringify(request)).toBe(original)
  })

  it.each([
    event('scheduled', { toolCallId: 'another', input: { command: 'wrong' } }),
    event('result', { input: { command: 'wrong' } }),
    event('scheduled', { input: ['wrong'] }),
  ])('rejects a supplemental input with another identity or an invalid shape: %j', (supplementalContent) => {
    const request = event('scheduled', { toolName: 'Bash', inputOmitted: true })
    const resolved = parsedMessageForRendering({ ...input(request), supplementalContent }, AgentProvider.ZCODE)
    expect(resolved.parentObject).toEqual(request)
  })

  it('keeps an explicit provider input when supplemental content also supplies one', () => {
    const request = event('scheduled', { toolName: 'Bash', input: { command: 'original' } })
    const resolved = parsedMessageForRendering({ ...input(request), supplementalContent: event('scheduled', { input: { command: 'stale' } }) }, AgentProvider.ZCODE)
    expect(resolved.parentObject).toEqual(request)
  })

  it('uses the shared read path and line range title', () => {
    const { container } = renderRequest('Read', { file_path: '/project/file.ts', offset: 10, limit: 5 })
    expect(container.querySelector(`.${toolInputPath}`)?.textContent).toBe('file.ts')
    expect(container.textContent).toContain('(Line 10–14)')
  })

  it('uses the shared write line count and edit statistics', () => {
    const write = renderRequest('Write', { file_path: '/project/file.ts', content: 'first\nsecond\n' })
    expect(write.container.querySelector(`.${toolInputPath}`)?.textContent).toBe('file.ts')
    expect(write.container.textContent).toContain('(2 lines)')
    const edit = renderRequest('Edit', { file_path: '/project/file.ts', old_string: 'before', new_string: 'after' })
    expect(edit.container.querySelector(`.${toolInputPath}`)?.textContent).toBe('file.ts')
    expect(edit.container.textContent).toContain('+1')
    expect(edit.container.textContent).toContain('-1')
  })

  it('uses the provider description for a command title', () => {
    const { container } = renderRequest('Bash', { command: 'printf marker', description: 'Print the fixture marker' })
    expect(container.textContent).toContain('Print the fixture marker')
    expect(container.textContent).toContain('printf marker')
  })

  it('shows a failed read as an error', () => {
    const { container, meta } = renderResult('Read', { file_path: '/project/missing.ts' }, { success: false, content: 'File does not exist' })
    expect(container.textContent).toContain('File does not exist')
    expect(container.textContent).toContain('Failed')
    expect(meta?.copyableContent()).toBe('File does not exist')
  })

  it('does not hide a failed todo update', () => {
    const { category, container } = renderResult('TodoWrite', { todos: [] }, { success: false, content: 'The task store is unavailable' })
    expect(category.kind).toBe('tool_result')
    expect(container.textContent).toContain('The task store is unavailable')
    expect(container.textContent).toContain('Failed')
  })

  it('skips malformed todo entries without losing valid tasks', () => {
    const { container } = renderRequest('TodoWrite', { todos: [null, false, { content: 'Valid task', status: 'pending' }] })
    expect(container.textContent).toContain('Valid task')
    expect(container.textContent).toContain('1 task')
  })

  it.each(['', 'Bash'])('renders an explicit file diff display for tool %s', (name) => {
    const { container, meta } = renderResult(name, {}, { success: true, display: { kind: 'file_diff', filePath: '/project/file.ts', truncated: true, structuredPatch: [{ oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: ['-before', '+after'] }] } })
    expect(container.querySelector(`.${diffAdded}`)?.textContent).toContain('after')
    expect(meta?.hasDiff).toBe(true)
    expect(meta?.copyableContent()).toContain('+after')
    expect(container.textContent).toContain('Result truncated')
  })

  it('copies file content without the native line-number prefixes', () => {
    const { meta } = renderResult('Read', { file_path: '/project/file.ts' }, { success: true, content: '1\tfirst\n2\tsecond' })
    expect(meta?.copyableContent()).toBe('first\nsecond')
  })

  it('renders native glob results with their truncation notice', () => {
    const { container } = renderResult('Glob', { pattern: '*.ts' }, { content: 'first.ts\nsecond.ts\n(Results are truncated. Consider using a more specific path or pattern.)' })
    expect(container.textContent).toContain('Found 2 files')
    expect(container.textContent).toContain('first.ts')
    expect(container.textContent).toContain('Output truncated')
  })

  it('renders grep count mode with native totals', () => {
    const { container } = renderResult('Grep', { pattern: 'answer', output_mode: 'count' }, { content: 'first.ts:2\nsecond.ts:1\n\nFound 3 total occurrences across 2 files. with pagination = limit: 250, offset: 0' })
    expect(container.textContent).toContain('3 matches in 2 files')
    expect(container.textContent).toContain('first.ts:2')
    expect(container.textContent).toContain('limit: 250')
  })

  it('renders fetched Markdown without inventing an HTTP status', () => {
    const { container } = renderResult('WebFetch', { url: 'https://example.com' }, { content: '## Page title\n\n**Page body**' })
    expect(container.querySelector('h2')?.textContent).toBe('Page title')
    expect(container.querySelector('strong')?.textContent).toBe('Page body')
    expect(container.textContent).not.toContain('200')
  })

  it('collapses unknown tool output consistently with its toolbar metadata', () => {
    const output = Array.from({ length: COLLAPSED_RESULT_ROWS + 3 }, (_, index) => `result line ${index}`).join('\n')
    const collapsed = renderResult('FutureTool', {}, { content: output })
    const expanded = renderResult('FutureTool', {}, { content: output }, true)
    expect(collapsed.meta?.collapsible).toBe(true)
    expect(collapsed.container.textContent).not.toContain(`result line ${COLLAPSED_RESULT_ROWS + 2}`)
    expect(expanded.container.textContent).toContain(`result line ${COLLAPSED_RESULT_ROWS + 2}`)
  })
})
