import type { ToolMessageSource } from './toolPresentation'
import { fireEvent, render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
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
