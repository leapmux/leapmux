import type { ToolCallIR } from '~/components/chat/ir/toolCall'
import type { RenderContext } from '~/components/chat/messageRenderers'
import type { ImageResultSource } from '~/lib/imageBlocks'
import type { ToolProgressEntry } from '~/stores/chatToolProgress'
import { render } from '@solidjs/testing-library'
import { beforeAll, describe, expect, it } from 'vitest'
import { imagesForIR } from '~/components/chat/ir/derivations'
import { failedResult } from '~/components/chat/ir/toolCall'
import { ToolMessage } from '~/components/chat/results/ToolMessage'
import { toolCallIr, toolRow } from '~/test-support/toolCallIr'

// jsdom does not provide ResizeObserver, which the shared layouts observe with.
beforeAll(() => {
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
})

function progress(outputTail: string | undefined) {
  return (): ToolProgressEntry | undefined =>
    outputTail === undefined ? undefined : { outputTail }
}

describe('toolCallMessage', () => {
  it('drops the header on a result row whose request row is beside it', () => {
    const call = toolCallIr('read', { request: { path: '/p/a.ts' }, result: { lines: null, fallbackContent: 'body' } })
    // The layout draws the body alone when the request row holds the header: the
    // wrapper that carries the tool-message hook is the header's own.
    const paired = render(() => <ToolMessage row={toolRow(call, 'result', { request: true })} />)
    expect(paired.container.querySelector('[data-tool-message]')).toBeNull()
    expect(paired.container.textContent).toContain('body')

    const alone = render(() => <ToolMessage row={toolRow(call, 'result', { request: false })} />)
    expect(alone.container.querySelector('[data-tool-message]')).not.toBeNull()
  })

  it('draws the live tail of a call that has not returned', () => {
    const call = toolCallIr('execute', { status: 'in_progress' })
    const liveTail = progress('streaming bytes')
    const { container } = render(() => <ToolMessage row={toolRow(call)} progress={{ liveTail }} />)
    expect(container.textContent).toContain('streaming bytes')
  })

  it('states truncation when the provider kept only part of the output', () => {
    // The call's own flag is RESULT-side, so the row that carries it has finished.
    const finished = toolCallIr('execute', { truncated: true })
    expect(render(() => <ToolMessage row={toolRow(finished)} />).container.textContent).toMatch(/truncated/i)

    // A call still RUNNING says it through the live tail instead, which is the one
    // route an unfinished row has to the notice.
    const running = toolCallIr('execute', { status: 'in_progress' })
    expect(render(() => <ToolMessage row={toolRow(running)} progress={{ liveTail: () => ({ outputTail: 'streaming bytes', outputTruncated: true }) }} />).container.textContent).toMatch(/truncated/i)
  })

  it('draws the outcome header above a prose body, and none above a status body', () => {
    const failedFetch = toolCallIr('fetch', { status: 'failed', result: failedResult('boom') })
    const fetch = render(() => <ToolMessage row={toolRow(failedFetch)} />)
    expect(fetch.container.textContent).toContain('Error')

    const stoppedTask = toolCallIr('task', { status: 'completed', result: { title: 'Stopped task-1', outcome: 'stopped', output: 'The task stopped.' } })
    const task = render(() => <ToolMessage row={toolRow(stoppedTask)} />)
    expect(task.container.textContent).toContain('Stopped task-1')
  })

  it('states the outcome when a kind that draws its own outcome drew nothing', () => {
    // Each of the three reads its outcome out of a LIST or an optional word, so an
    // empty one leaves the row with a title and silence. The shared header is then
    // the only thing that can say the call failed.
    const agent = render(() => <ToolMessage row={toolRow(toolCallIr('agent', { status: 'failed', result: { agents: [] } }))} />)
    expect(agent.container.textContent).toContain('Error')

    const execute = render(() => <ToolMessage row={toolRow(toolCallIr('execute', { status: 'failed', result: { commands: [], unresolvedTerminals: [] } }))} />)
    expect(execute.container.textContent).toContain('Error')

    const task = render(() => <ToolMessage row={toolRow(toolCallIr('task', { status: 'failed', result: { outcome: 'failed', output: '' } }))} />)
    expect(task.container.textContent).toContain('Error')
  })

  // A card whose run has not ENDED draws the neutral glyph and the child's own word.
  // It says what the subagent is, never how the call finished, so the shared header
  // must still state that -- Codex reports a launch whose child state never arrived as
  // `status unavailable`, and the row was then entirely about the child.
  it('states the outcome when an agent card has not ended', () => {
    const unknown = { description: '', agentId: 'thread-1', statusLabel: 'status unavailable', outcome: 'unknown' as const, metadata: [], body: '' }
    const pending = render(() => <ToolMessage row={toolRow(toolCallIr('agent', { status: 'failed', result: { agents: [unknown] } }))} />)
    expect(pending.container.textContent).toContain('status unavailable')
    expect(pending.container.textContent).toContain('Error')

    // ONE unended card among several leaves the row incomplete, so the header draws.
    const ended = { description: '', agentId: 'thread-2', statusLabel: 'failed', outcome: 'failed' as const, metadata: [], body: 'it broke' }
    const mixed = render(() => <ToolMessage row={toolRow(toolCallIr('agent', { status: 'failed', result: { agents: [ended, unknown] } }))} />)
    expect(mixed.container.textContent).toContain('Error')
  })

  it('keeps the shared outcome header away from a body that states the outcome', () => {
    const agents = [{ description: 'Fix the build', agentId: 'a1', statusLabel: 'failed', outcome: 'failed' as const, metadata: [], body: 'it broke' }]
    const { container } = render(() => <ToolMessage row={toolRow(toolCallIr('agent', { status: 'failed', result: { agents } }))} />)
    expect(container.textContent).toContain('it broke')
    expect(container.textContent).not.toContain('Error')
  })

  it('uses the agent prompt expand key on an agent request row', () => {
    const call = toolCallIr('agent', { status: 'in_progress', request: { description: 'Fix the build', prompt: 'Run the tests.' } })
    const { container } = render(() => <ToolMessage row={toolRow(call, 'request', { result: false })} />)
    expect(container.textContent).toContain('Fix the build')
    expect(container.textContent).toContain('Run the tests.')
  })
})

/**
 * An image TAB addresses a picture by its index in `imagesForIR`, and the row hands
 * that same index to `onOpenImage`. The two are one order or a tab opens the wrong
 * picture, and it survives a reload, when the tab resolves its index against the
 * message re-fetched from the worker.
 *
 * The row draws the RESULT body first, the call's extra content under it, then the
 * call's own pictures. An offset of zero on the extra content gave two pictures the
 * same number.
 */
const picture = (name: string): ImageResultSource => ({ mimeType: 'image/png', data: name })

/**
 * The RESULT side of one call: its extra content, its own pictures, and the notice
 * that says the provider kept only part of the output.
 *
 * A span whose two rows are both drawn puts the result body on the result row alone,
 * and `rowDrawsResult` is that rule. Everything the result side carries follows it, or
 * a paired request row draws the answer a second time -- and `imagesForIR` reports NO
 * picture for that row, so the index it handed `onOpenImage` addressed a picture the
 * reader never saw.
 */
describe('the result side of a paired tool span (ToolMessage)', () => {
  const call = toolCallIr('read', {
    request: { path: '/p/a.ts' },
    result: { lines: null, fallbackContent: 'the file body' },
    extraContent: [{ type: 'text', text: 'rich extra content' }, { type: 'image', source: picture('EXTRA') }],
    images: [picture('OWN')],
    truncated: true,
  })
  const requestRow = toolRow(call, 'request', { result: true })
  const resultRow = toolRow(call, 'result', { request: true })

  // `ImageResultView` draws a bare `<img>` without `onOpenImage`, so every case here
  // renders with one: the index each picture reports is the whole point.
  function drawRow(row: typeof requestRow): { container: HTMLElement, opened: number[], drawn: string[] } {
    const opened: number[] = []
    const context = { images: { loadFileImage: () => Promise.resolve(undefined), cachedFileImage: () => undefined, openImage: (request: { index: number }) => opened.push(request.index), deferLoad: () => false, premeasurePass: () => false } } as unknown as RenderContext
    const { container } = render(() => <ToolMessage row={row} context={context} />)
    const buttons = [...container.querySelectorAll('button[aria-label="Open image"]')]
    const drawn = buttons.map(button => button.querySelector('img')?.getAttribute('src') ?? '')
    for (const button of buttons)
      (button as HTMLButtonElement).click()
    return { container, opened, drawn }
  }

  it('draws no result-side content on a request row whose result row is beside it', () => {
    const { container, opened } = drawRow(requestRow)
    expect(container.textContent).not.toContain('rich extra content')
    expect(container.textContent).not.toContain('the file body')
    expect(container.textContent).not.toMatch(/truncated/i)
    expect(opened).toEqual([])
  })

  it('draws each result-side item exactly once on the result row', () => {
    const { container, opened } = drawRow(resultRow)
    expect(container.textContent?.match(/rich extra content/g)).toHaveLength(1)
    expect(container.textContent?.match(/the file body/g)).toHaveLength(1)
    expect(opened).toEqual([0, 1])
  })

  it('opens each picture at the index `imagesForIR` reports for the row that drew it', () => {
    for (const row of [requestRow, resultRow]) {
      const listed = imagesForIR(row)
      const { opened, drawn } = drawRow(row)
      expect(opened).toEqual(listed.map((_source, index) => index))
      expect(drawn).toEqual(listed.map(source => `data:image/png;base64,${source.data}`))
    }
  })

  it('reports only indexes that still address the same picture after a reload', () => {
    // An image tab keeps (seq, index) and re-resolves it through
    // `messageToolResultImages`, which reads the FINISHED side of the span. An index a
    // row reported that the finished side does not hold resolves to a different
    // picture, or to none, once the reader reloads the transcript.
    const afterReload = imagesForIR(toolRow(call, 'result'))
    for (const row of [requestRow, resultRow]) {
      const { opened } = drawRow(row)
      expect(opened.map(index => afterReload[index]?.data))
        .toEqual(imagesForIR(row).map(source => source.data))
    }
  })
})

/**
 * The three SOURCES a row draws pictures from, each at the index `imagesForIR`
 * reports for it: the pictures a generic result holds in its own content blocks, the
 * call's extra content under that, and the call's own pictures last.
 *
 * Two calls, because no single one reaches all three. Only a generic result carries
 * content blocks, and a generic kind carries no pictures of its own -- invariant I6,
 * which states that its pictures ride in those blocks instead. Between the pair every
 * source is drawn, and each boundary between two of them is crossed.
 */
describe('the picture a tool row opens (ToolMessage)', () => {
  it('opens the picture that `imagesForIR` holds at the index it reports', () => {
    const cases: { call: ToolCallIR, expected: string[] }[] = [
      {
        call: toolCallIr('mcp', {
          result: { content: [{ type: 'text', text: 'before' }, { type: 'image', source: picture('RESULT') }] },
          extraContent: [{ type: 'image', source: picture('EXTRA') }],
        }),
        expected: ['RESULT', 'EXTRA'],
      },
      {
        call: toolCallIr('read', {
          result: { lines: null, fallbackContent: 'the file body' },
          extraContent: [{ type: 'image', source: picture('EXTRA') }],
          images: [picture('OWN')],
        }),
        expected: ['EXTRA', 'OWN'],
      },
    ]

    for (const { call, expected } of cases) {
      const row = toolRow(call)
      const listed = imagesForIR(row)
      expect(listed.map(source => source.data)).toEqual(expected)

      const opened: number[] = []
      const context = { images: { loadFileImage: () => Promise.resolve(undefined), cachedFileImage: () => undefined, openImage: (request: { index: number }) => opened.push(request.index), deferLoad: () => false, premeasurePass: () => false } } as unknown as RenderContext
      const { container } = render(() => <ToolMessage row={row} context={context} />)

      const buttons = [...container.querySelectorAll('button[aria-label="Open image"]')]
      expect(buttons).toHaveLength(expected.length)
      const drawn = buttons.map(button => button.querySelector('img')?.getAttribute('src') ?? '')
      for (const button of buttons)
        (button as HTMLButtonElement).click()

      // Each button reports its own place in the list, and the picture at that place is
      // the one the button draws.
      expect(opened).toEqual(listed.map((_source, index) => index))
      expect(drawn).toEqual(listed.map(source => `data:image/png;base64,${source.data}`))
    }
  })
})
