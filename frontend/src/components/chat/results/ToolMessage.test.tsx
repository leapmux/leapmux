import type { ToolMessageSource } from './toolPresentation'
import { fireEvent, render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { toolOutcomeLabel } from '../toolOutcomeLabel'
import { TRUNCATION_NOTICE } from '../truncationNotice'
import { ToolMessage } from './ToolMessage'
import '../providers/testMocks'

function source(id: string, role: ToolMessageSource['role']): ToolMessageSource {
  return {
    id,
    role,
    status: role === 'result' ? 'completed' : 'pending',
    images: [],
    presentation: {
      kind: 'read',
      title: 'Read source',
      input: { path: '/project/example.txt' },
      output: role === 'result' ? 'Actual provider output' : '',
      body: { type: 'text' },
      unresolvedTerminals: [],
    },
  }
}

describe('shared tool message', () => {
  it('uses distinct image indices across rich content and separate images', () => {
    const onOpenImage = vi.fn()
    const current = source('tool', 'result')
    current.images = [{ mimeType: 'image/png', data: 'c2Vjb25k' }]
    current.presentation.additionalContent = {
      server: '',
      tool: 'Tool',
      argsJson: '',
      status: 'completed',
      content: [{ type: 'image', source: { mimeType: 'image/png', data: 'Zmlyc3Q=' } }],
    }
    const { container } = render(() => <ToolMessage source={current} context={{ onOpenImage }} />)
    const images = container.querySelectorAll('img')
    expect(images).toHaveLength(2)
    fireEvent.click(images[0].closest('button')!)
    fireEvent.click(images[1].closest('button')!)
    expect(onOpenImage.mock.calls.map(([request]) => request.index)).toEqual([0, 1])
  })

  // The MCP row draws through McpToolMessage, which took only the body source. The
  // accompanying content never reached the reader, while `toolPresentationMeta` still
  // counted it in the Copy text and in the collapsible answer.
  it('draws the content that accompanies an MCP body', () => {
    const onOpenImage = vi.fn()
    const current = source('tool', 'result')
    current.presentation.body = {
      type: 'mcp',
      source: {
        server: 'files',
        tool: 'read',
        argsJson: '',
        status: 'completed',
        content: [
          { type: 'text', text: 'the MCP result body' },
          { type: 'image', source: { mimeType: 'image/png', data: 'c2Vjb25k' } },
        ],
      },
    }
    current.presentation.additionalContent = {
      server: '',
      tool: 'Tool',
      argsJson: '',
      status: 'completed',
      content: [
        { type: 'text', text: 'a note neither field carries' },
        { type: 'image', source: { mimeType: 'image/png', data: 'Zmlyc3Q=' } },
      ],
    }
    const { container } = render(() => <ToolMessage source={current} context={{ onOpenImage }} />)
    expect(container.textContent).toContain('the MCP result body')
    expect(container.textContent).toContain('a note neither field carries')
    // `acpToolResultImages` lists the accompanying images first, so the two bodies
    // must not both start numbering at zero.
    const images = container.querySelectorAll('img')
    expect(images).toHaveLength(2)
    fireEvent.click(images[0].closest('button')!)
    fireEvent.click(images[1].closest('button')!)
    expect(onOpenImage.mock.calls.map(([request]) => request.index).sort()).toEqual([0, 1])
  })

  it.each(['', 'another-tool'])('keeps a result header when its request has identity %j', (requestID) => {
    const { container } = render(() => <ToolMessage source={source('tool', 'result')} request={source(requestID, 'request')} />)
    expect(container.textContent).toContain('example.txt')
    expect(container.textContent).toContain('Actual provider output')
  })

  it('removes the duplicate header when a matching request arrives', () => {
    const [request, setRequest] = createSignal<ToolMessageSource>()
    const { container } = render(() => <ToolMessage source={source('tool', 'result')} request={request()} />)
    expect(container.textContent).toContain('example.txt')
    setRequest(source('tool', 'request'))
    expect(container.textContent).not.toContain('example.txt')
    expect(container.textContent).toContain('Actual provider output')
    setRequest(undefined)
    expect(container.textContent).toContain('example.txt')
  })

  it('keeps the result header when the matching source is already a result', () => {
    const { container } = render(() => <ToolMessage source={source('tool', 'result')} request={source('tool', 'result')} />)
    expect(container.textContent).toContain('example.txt')
    expect(container.textContent).toContain('Actual provider output')
  })
})

// Cursor's `ReadLints` declares ACP kind `read`, sends `title: "Read Lints"`, and
// gives the WORKING DIRECTORY as its only location. Titling the row with that path
// drew a row whose entire label was ".", and the title the provider sent never
// reached the reader. See RL-042.
describe('a read whose path is the working directory', () => {
  const lintRow = (): ToolMessageSource => ({
    id: 'lints',
    role: 'request',
    status: 'pending',
    images: [],
    presentation: {
      kind: 'read',
      title: 'Read Lints',
      input: { path: '/project' },
      output: '',
      body: { type: 'text' },
      unresolvedTerminals: [],
    },
  })

  it('states the tool title rather than a bare dot', () => {
    const { container } = render(() => (
      <ToolMessage source={lintRow()} context={{ workingDir: '/project' }} />
    ))
    expect(container.textContent).toContain('Read Lints')
    expect(container.textContent?.trim()).not.toBe('.')
  })

  // A read of a real file still names the file, which is what the row is for.
  it('still names a file it actually read', () => {
    const row = lintRow()
    row.presentation.input = { path: '/project/src/main.ts' }
    const { container } = render(() => (
      <ToolMessage source={row} context={{ workingDir: '/project' }} />
    ))
    expect(container.textContent).toContain('src/main.ts')
  })
})

// Reasonix reports ACP kind `move` for a rename, and three of the four shared
// tables had no entry for it: the row drew the generic wrench and repeated its
// input as raw JSON under the header. See DESIGN-FE-2.
describe('a move that reaches the shared row', () => {
  const moveRow = (input: Record<string, unknown>): ToolMessageSource => ({
    id: 'move',
    role: 'request',
    status: 'pending',
    images: [],
    presentation: {
      kind: 'move',
      title: 'move_file',
      input,
      output: '',
      body: { type: 'text' },
      unresolvedTerminals: [],
    },
  })

  it('states the tool name rather than dumping its input as JSON', () => {
    const { container } = render(() => (
      <ToolMessage source={moveRow({ source_path: '/project/a.ts', destination_path: '/project/b.ts' })} context={{ workingDir: '/project' }} />
    ))
    expect(container.textContent).toContain('move_file')
    expect(container.textContent).not.toContain('destination_path')
  })

  it('states the destination when the provider used a shared path key', () => {
    const { container } = render(() => (
      <ToolMessage source={moveRow({ filePath: '/project/src/b.ts' })} context={{ workingDir: '/project' }} />
    ))
    expect(container.textContent).toContain('src/b.ts')
  })

  it('draws its own icon rather than the unclassified one', () => {
    const generic = render(() => <ToolMessage source={{ ...moveRow({}), presentation: { ...moveRow({}).presentation, kind: 'other' } }} />)
    const move = render(() => <ToolMessage source={moveRow({})} />)
    const pathsOf = (root: HTMLElement) => root.querySelector('svg')?.innerHTML
    expect(pathsOf(move.container as HTMLElement)).toBeTruthy()
    expect(pathsOf(move.container as HTMLElement)).not.toBe(pathsOf(generic.container as HTMLElement))
  })
})

describe('shared tool message truncation notice', () => {
  function row(presentation: Partial<ToolMessageSource['presentation']>): ToolMessageSource {
    const base = source('tool', 'result')
    return { ...base, presentation: { ...base.presentation, ...presentation } }
  }

  it('states that the provider cut the output', () => {
    const { container } = render(() => <ToolMessage source={row({ truncated: true })} />)
    expect(container.textContent).toContain(TRUNCATION_NOTICE)
  })

  it('stays quiet for a row the provider returned whole', () => {
    const { container } = render(() => <ToolMessage source={row({})} />)
    expect(container.textContent).not.toContain(TRUNCATION_NOTICE)
  })
})

describe('shared tool message status body', () => {
  // The status body draws its OWN outcome header, so the row's Error header above it
  // would state the same outcome a second time.
  it('draws no outcome header of its own above a status body', () => {
    const base = source('tool', 'result')
    const current: ToolMessageSource = {
      ...base,
      status: 'failed',
      presentation: { ...base.presentation, body: { type: 'status', source: { title: 'Failed to send', outcome: 'failed', output: 'Peer unavailable' } } },
    }
    const { container } = render(() => <ToolMessage source={current} />)
    expect(container.textContent).toContain('Failed to send')
    expect(container.textContent).not.toContain(toolOutcomeLabel('failed'))
  })

  it('draws its own outcome header above a plain text body', () => {
    const base = source('tool', 'result')
    const { container } = render(() => <ToolMessage source={{ ...base, status: 'failed' }} />)
    expect(container.textContent).toContain(toolOutcomeLabel('failed'))
  })
})
