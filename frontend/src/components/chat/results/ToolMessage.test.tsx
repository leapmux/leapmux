import type { ToolCall } from '~/components/chat/model/toolCall'
import type { ToolResultRenderContext } from '~/components/chat/renderContext'
import type { ImageResultSource } from '~/lib/imageBlocks'
import type { ToolProgressEntry } from '~/stores/chatToolProgress'
import { render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { failedResult } from '~/components/chat/model/toolCall'
import { imagesForRow } from '~/components/chat/results/rowImages'
import { ToolMessage } from '~/components/chat/results/ToolMessage'
import { toolCallFixture, toolRow } from '~/test-support/toolCallFixture'
import { nativeOutputPathsPrecedePreview } from '../../../../tests/e2e/helpers/nativeToolOutputFilePaths'
import { toolCallMeta } from './tools/meta'

function progress(outputTail: string | undefined) {
  return (): ToolProgressEntry | undefined =>
    outputTail === undefined ? undefined : { outputTail }
}

describe('ToolMessage', () => {
  it('renders reported output paths before the native preview without opening a file', () => {
    const call = Object.assign(toolCallFixture('execute', {
      result: { commands: [{ output: 'The original native preview.' }], unresolvedTerminals: [] },
    }), { outputFilePaths: ['/native/tool-output/result.log'] })

    const { container } = render(() => <ToolMessage row={toolRow(call)} />)

    expect(container.textContent).toContain('The original native preview.')
    expect(container.textContent).toContain('Output file:')
    expect(container.textContent).toContain('/native/tool-output/result.log')
    expect(container.querySelector('a')).toBeNull()
    expect(container.textContent?.indexOf('/native/tool-output/result.log'))
      .toBeLessThan(container.textContent?.indexOf('The original native preview.') ?? -1)
  })

  it.each([false, true])('keeps command properties before the output when expanded is %s', (expanded) => {
    const output = ['PROPERTY_ORDER_PREVIEW', ...Array.from({ length: 8 }, (_, index) => `native line ${index}`), 'PROPERTY_ORDER_LAST_LINE'].join('\n')
    const call = Object.assign(toolCallFixture('execute', {
      request: { command: 'printf native', cwd: '/native/workspace', processId: '4721' },
      result: { commands: [{ output }], unresolvedTerminals: [] },
      metadata: [{ label: 'Shell ID', value: 'owned-shell' }],
    }), { outputFilePaths: ['/native/output.log'] })
    const { container } = render(() => <ToolMessage row={toolRow(call)} context={{ getMessageUiState: () => expanded }} />)
    if (expanded) {
      expect(container.textContent).toContain('PROPERTY_ORDER_LAST_LINE')
    }
    const text = container.textContent ?? ''
    const preview = text.indexOf('PROPERTY_ORDER_PREVIEW')
    expect(preview).toBeGreaterThanOrEqual(0)
    for (const property of ['4721', '/native/workspace', 'owned-shell', '/native/output.log']) {
      expect(text.indexOf(property)).toBeGreaterThanOrEqual(0)
      expect(text.indexOf(property)).toBeLessThan(preview)
    }
    expect(container.querySelectorAll('[data-testid="tool-output-file-paths"]')).toHaveLength(1)
  })

  it('keeps agent properties and output paths before the report', () => {
    const call = Object.assign(toolCallFixture('agent', {
      result: {
        agents: [{ description: 'Read the module', agentId: 'native-child', outcome: 'completed', statusLabel: 'completed', metadata: [{ label: 'Agent ID', value: 'native-child' }, { label: 'Model', value: 'native-model' }], body: 'PROPERTY_ORDER_AGENT_PREVIEW' }],
      },
    }), { outputFilePaths: ['/native/agent-report.log'] })
    const { container } = render(() => <ToolMessage row={toolRow(call)} />)
    const text = container.textContent ?? ''
    const preview = text.indexOf('PROPERTY_ORDER_AGENT_PREVIEW')
    expect(preview).toBeGreaterThanOrEqual(0)
    for (const property of ['native-child', 'native-model', '/native/agent-report.log']) {
      expect(text.indexOf(property)).toBeGreaterThanOrEqual(0)
      expect(text.indexOf(property)).toBeLessThan(preview)
    }
  })

  it('renders output paths only on the result side of a paired span', () => {
    const call = Object.assign(toolCallFixture('execute', {
      result: { commands: [{ output: 'The native paired preview.' }], unresolvedTerminals: [] },
    }), { outputFilePaths: ['/native/tool-output/paired.log'] })

    const request = render(() => <ToolMessage row={toolRow(call, 'request', { result: true })} />)
    const result = render(() => <ToolMessage row={toolRow(call, 'result', { request: true })} />)

    expect(request.container.textContent).not.toContain('/native/tool-output/paired.log')
    expect(result.container.textContent).toContain('/native/tool-output/paired.log')
    expect(result.container.textContent).toContain('The native paired preview.')
  })

  it('updates output paths when later native metadata reaches the result', () => {
    const original = toolCallFixture('execute', {
      result: { commands: [{ output: 'The original asynchronous preview.' }], unresolvedTerminals: [] },
    })
    const updated = Object.assign(toolCallFixture('execute', {
      result: { commands: [{ output: 'The original asynchronous preview.' }], unresolvedTerminals: [] },
    }), { outputFilePaths: ['/native/tool-output/completed.log'] })
    const [call, setCall] = createSignal<ToolCall>(original)
    const { container } = render(() => <ToolMessage row={toolRow(call())} />)
    expect(container.textContent).not.toContain('/native/tool-output/completed.log')

    setCall(updated)

    expect(container.textContent).toContain('/native/tool-output/completed.log')
    expect(container.textContent).toContain('The original asynchronous preview.')
  })

  it('drops the header on a result row whose request row is beside it', () => {
    const call = toolCallFixture('read', { request: { path: '/p/a.ts' }, result: { lines: null, fallbackContent: 'body' } })
    // The layout draws only the body when the request row owns the header.
    // The tool-message attribute belongs to that header's wrapper.
    const paired = render(() => <ToolMessage row={toolRow(call, 'result', { request: true })} />)
    expect(paired.container.querySelector('[data-tool-message]')).toBeNull()
    expect(paired.container.textContent).toContain('body')

    const alone = render(() => <ToolMessage row={toolRow(call, 'result', { request: false })} />)
    expect(alone.container.querySelector('[data-tool-message]')).not.toBeNull()
  })

  it('draws the live tail of a call that has not returned', () => {
    const call = toolCallFixture('execute', { status: 'in_progress' })
    const liveTail = progress('streaming bytes')
    const { container } = render(() => <ToolMessage row={toolRow(call)} progress={{ liveTail }} />)
    expect(container.textContent).toContain('streaming bytes')
  })

  it('states truncation when the provider kept only part of the output', () => {
    // The completed result carries the call's truncation flag.
    const finished = toolCallFixture('execute', { truncated: true })
    expect(render(() => <ToolMessage row={toolRow(finished)} />).container.textContent).toMatch(/truncated/i)

    // An unfinished result receives its truncation notice only through the live tail.
    const running = toolCallFixture('execute', { status: 'in_progress' })
    expect(render(() => <ToolMessage row={toolRow(running)} progress={{ liveTail: () => ({ outputTail: 'streaming bytes', outputTruncated: true }) }} />).container.textContent).toMatch(/truncated/i)
  })

  it('draws the outcome header above a prose body, and none above a status body', () => {
    const failedFetch = toolCallFixture('fetch', { status: 'failed', result: failedResult('boom') })
    const fetch = render(() => <ToolMessage row={toolRow(failedFetch)} />)
    expect(fetch.container.textContent).toContain('Error')

    const stoppedTask = toolCallFixture('task', { status: 'completed', result: { title: 'Stopped task-1', outcome: 'stopped', output: 'The task stopped.' } })
    const task = render(() => <ToolMessage row={toolRow(stoppedTask)} />)
    expect(task.container.textContent).toContain('Stopped task-1')
  })

  it('states the outcome when a kind that draws its own outcome drew nothing', () => {
    // These readers use a list or optional value to determine the outcome.
    // Empty values leave no outcome text. The shared header must state the failure.
    const agent = render(() => <ToolMessage row={toolRow(toolCallFixture('agent', { status: 'failed', result: { agents: [] } }))} />)
    expect(agent.container.textContent).toContain('Error')

    const execute = render(() => <ToolMessage row={toolRow(toolCallFixture('execute', { status: 'failed', result: { commands: [], unresolvedTerminals: [] } }))} />)
    expect(execute.container.textContent).toContain('Error')

    const task = render(() => <ToolMessage row={toolRow(toolCallFixture('task', { status: 'failed', result: { outcome: 'failed', output: '' } }))} />)
    expect(task.container.textContent).toContain('Error')
  })

  // An unfinished card shows a neutral icon and the child's status.
  // The shared header must state how the call ended.
  // Codex reports status unavailable when the child's state never arrives.
  it('states the outcome when an agent card has not ended', () => {
    const unknown = { description: '', agentId: 'thread-1', statusLabel: 'status unavailable', outcome: 'unknown' as const, metadata: [], body: '' }
    const pending = render(() => <ToolMessage row={toolRow(toolCallFixture('agent', { status: 'failed', result: { agents: [unknown] } }))} />)
    expect(pending.container.textContent).toContain('status unavailable')
    expect(pending.container.textContent).toContain('Error')

    // One unfinished card leaves the row incomplete, so the header still appears.
    const ended = { description: '', agentId: 'thread-2', statusLabel: 'failed', outcome: 'failed' as const, metadata: [], body: 'it broke' }
    const mixed = render(() => <ToolMessage row={toolRow(toolCallFixture('agent', { status: 'failed', result: { agents: [ended, unknown] } }))} />)
    expect(mixed.container.textContent).toContain('Error')
  })

  it('keeps the shared outcome header away from a body that states the outcome', () => {
    const agents = [{ description: 'Fix the build', agentId: 'a1', statusLabel: 'failed', outcome: 'failed' as const, metadata: [], body: 'it broke' }]
    const { container } = render(() => <ToolMessage row={toolRow(toolCallFixture('agent', { status: 'failed', result: { agents } }))} />)
    expect(container.textContent).toContain('it broke')
    expect(container.textContent).not.toContain('Error')
  })

  it('uses the agent prompt expand key on an agent request row', () => {
    const call = toolCallFixture('agent', { status: 'in_progress', request: { description: 'Fix the build', prompt: 'Run the tests.' } })
    const { container } = render(() => <ToolMessage row={toolRow(call, 'request', { result: false })} />)
    expect(container.textContent).toContain('Fix the build')
    expect(container.textContent).toContain('Run the tests.')
  })
})

/**
 * An image tab uses the index from imagesForRow. The row passes that same index to onOpenImage.
 * Different orders open the wrong image. Reload keeps that error when the tab resolves its index against the stored message.
 *
 * The row draws result images first. Extra content and the call's images follow in that order.
 * An extra-content offset of zero gives two images the same index.
 */
const picture = (name: string): ImageResultSource => ({ mimeType: 'image/png', data: name })

/** Supply image actions so each button reports its index. Without them, the view draws only an image. */
function drawToolImages(row: ReturnType<typeof toolRow>): { container: HTMLElement, opened: number[], drawn: string[] } {
  const opened: number[] = []
  const context: ToolResultRenderContext = { images: { loadFileImage: () => Promise.resolve(undefined), cachedFileImage: () => undefined, openImage: request => opened.push(request.index), deferLoad: () => false, premeasurePass: () => false } }
  const { container } = render(() => <ToolMessage row={row} context={context} />)
  const buttons = [...container.querySelectorAll<HTMLButtonElement>('button[aria-label="Open image"]')]
  const drawn = buttons.map(button => button.querySelector('img')?.getAttribute('src') ?? '')
  for (const button of buttons)
    button.click()
  return { container, opened, drawn }
}

/**
 * The result row owns extra content and images. It owns the notice for truncated output also.
 *
 * rowDrawsResult assigns result content to the result row when both span rows appear.
 * Every result item must follow that rule. Otherwise, the request row draws duplicate content and reports an image index that it cannot resolve.
 */
describe('the result side of a paired tool span (ToolMessage)', () => {
  const call = toolCallFixture('read', {
    request: { path: '/p/a.ts' },
    result: { lines: null, fallbackContent: 'the file body' },
    extraContent: [{ type: 'text', text: 'rich extra content' }, { type: 'image', source: picture('EXTRA') }],
    images: [picture('OWN')],
    truncated: true,
  })
  const requestRow = toolRow(call, 'request', { result: true })
  const resultRow = toolRow(call, 'result', { request: true })

  it('draws no result-side content on a request row whose result row is beside it', () => {
    const { container, opened } = drawToolImages(requestRow)
    expect(container.textContent).not.toContain('rich extra content')
    expect(container.textContent).not.toContain('the file body')
    expect(container.textContent).not.toMatch(/truncated/i)
    expect(opened).toEqual([])
  })

  it('draws each result-side item exactly once on the result row', () => {
    const { container, opened } = drawToolImages(resultRow)
    expect(container.textContent?.match(/rich extra content/g)).toHaveLength(1)
    expect(container.textContent?.match(/the file body/g)).toHaveLength(1)
    expect(opened).toEqual([0, 1])
  })

  it('opens each picture at the index `imagesForRow` reports for the row that drew it', () => {
    for (const row of [requestRow, resultRow]) {
      const listed = imagesForRow(row)
      const { opened, drawn } = drawToolImages(row)
      expect(opened).toEqual(listed.map((_source, index) => index))
      expect(drawn).toEqual(listed.map(source => `data:image/png;base64,${source.data}`))
    }
  })

  it('reports only indexes that still address the same picture after a reload', () => {
    // An image tab stores (seq, index). messageToolResultImages resolves that pair from the completed result.
    // A different index order resolves to another image or no image after reload.
    const afterReload = imagesForRow(toolRow(call, 'result'))
    for (const row of [requestRow, resultRow]) {
      const { opened } = drawToolImages(row)
      expect(opened.map(index => afterReload[index]?.data))
        .toEqual(imagesForRow(row).map(source => source.data))
    }
  })
})

/**
 * imagesForRow supplies the index for each image source:
 * - The generic result's content blocks.
 * - The call's extra content.
 * - The call's own images.
 *
 * No single call reaches all three sources. Only a generic result carries content blocks.
 * Invariant I6 puts generic images in those blocks, so a generic call carries no separate images.
 * These two calls cover every source and each boundary between sources.
 */
describe('the picture a tool row opens (ToolMessage)', () => {
  it('opens the picture that `imagesForRow` holds at the index it reports', () => {
    const cases: { call: ToolCall, expected: string[] }[] = [
      {
        call: toolCallFixture('mcp', {
          result: { content: [{ type: 'text', text: 'before' }, { type: 'image', source: picture('RESULT') }] },
          extraContent: [{ type: 'image', source: picture('EXTRA') }],
        }),
        expected: ['RESULT', 'EXTRA'],
      },
      {
        call: toolCallFixture('read', {
          result: { lines: null, fallbackContent: 'the file body' },
          extraContent: [{ type: 'image', source: picture('EXTRA') }],
          images: [picture('OWN')],
        }),
        expected: ['EXTRA', 'OWN'],
      },
    ]

    for (const { call, expected } of cases) {
      const row = toolRow(call)
      const listed = imagesForRow(row)
      expect(listed.map(source => source.data)).toEqual(expected)

      const { container, opened, drawn } = drawToolImages(row)
      expect(container.querySelectorAll('button[aria-label="Open image"]')).toHaveLength(expected.length)

      // Each button reports the index of the image that it draws.
      expect(opened).toEqual(listed.map((_source, index) => index))
      expect(drawn).toEqual(listed.map(source => `data:image/png;base64,${source.data}`))
    }
  })
})

describe('ToolMessage output DOM ownership', () => {
  const marker = 'NATIVE_OUTPUT_OWNER_MARKER'
  const path = '/native/owned-output.log'
  const metadataJson = '{"calls":[{"args":"NATIVE_OUTPUT_OWNER_MARKER"}]}'

  it('does not accept the real argument and metadata surfaces as returned output', () => {
    const call = Object.assign(toolCallFixture('mcp', { request: { server: '', tool: 'codemode', args: { code: `text("${marker}")` } }, result: { content: [], structuredJson: metadataJson, structuredJsonRole: 'metadata' } }), { outputFilePaths: [path] })
    const row = toolRow(call)
    const { container } = render(() => <ToolMessage row={row} context={{ getMessageUiState: () => true }} />)
    expect(container.textContent).toContain(marker)
    expect(container.textContent).toContain(metadataJson)
    expect(container.querySelector('[data-tool-output-preview]')).toBeNull()
    expect(nativeOutputPathsPrecedePreview([container], [marker])).toBe(false)
    expect(toolCallMeta(row).copyableContent()).toBe(metadataJson)
  })

  it.each([false, true])('keeps metadata and paths before actual output when expanded is %s', (expanded) => {
    const output = [marker, ...Array.from({ length: 8 }, (_, index) => `native line ${index}`), 'NATIVE_OUTPUT_OWNER_LAST'].join('\n')
    const call = Object.assign(toolCallFixture('mcp', { request: { server: '', tool: 'codemode', args: { code: `text("${marker}")` } }, result: { content: [{ type: 'text', text: output }], structuredJson: metadataJson, structuredJsonRole: 'metadata' } }), { outputFilePaths: [path] })
    const row = toolRow(call)
    const { container } = render(() => <ToolMessage row={row} context={{ getMessageUiState: () => expanded }} />)
    const outputs = container.querySelectorAll('[data-tool-output-preview]')
    expect(outputs).toHaveLength(1)
    expect(outputs[0]?.textContent).toContain(marker)
    // The metadata and the path text can hold the marker too, so compare element positions, not text offsets.
    const metadataBlock = [...container.querySelectorAll('div')].find(element => element.textContent === metadataJson)
    const pathBlock = container.querySelector('[data-testid="tool-output-file-paths"]')
    if (!metadataBlock || !pathBlock || !outputs[0])
      throw new Error('The ownership fixture requires its metadata, path, and output blocks.')
    expect(metadataBlock.compareDocumentPosition(outputs[0]) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0)
    expect(pathBlock.compareDocumentPosition(outputs[0]) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0)
    expect(nativeOutputPathsPrecedePreview([container], [marker])).toBe(true)
    expect(toolCallMeta(row).copyableContent()).toBe(`${output}\n\n${metadataJson}`)
  })

  it('owns a genuine structured-only tool result beside the path', () => {
    const structuredJson = '{"count":0,"enabled":false,"nullable":null}'
    const call = Object.assign(toolCallFixture('mcp', { request: { args: { text: structuredJson }, server: 'native', tool: 'inspect' }, result: { content: [], structuredJson } }), { outputFilePaths: [path] })
    const row = toolRow(call)
    const { container } = render(() => <ToolMessage row={row} />)
    expect(container.querySelectorAll('[data-tool-output-preview]')).toHaveLength(1)
    expect(nativeOutputPathsPrecedePreview([container], ['"count":0'])).toBe(true)
    expect(toolCallMeta(row).copyableContent()).toBe(structuredJson)
  })

  it('keeps output ownership off a paired request row and on its result row', () => {
    const call = Object.assign(toolCallFixture('mcp', { result: { content: [{ type: 'text', text: marker }] } }), { outputFilePaths: [path] })
    const request = render(() => <ToolMessage row={toolRow(call, 'request', { result: true })} />)
    const result = render(() => <ToolMessage row={toolRow(call, 'result', { request: true })} />)
    expect(request.container.querySelector('[data-tool-output-preview]')).toBeNull()
    expect(result.container.querySelector('[data-tool-output-preview]')?.textContent).toBe(marker)
    expect(nativeOutputPathsPrecedePreview([result.container], [marker])).toBe(true)
  })

  it('marks failure prose and live native output without marking the outcome header', () => {
    const failed = render(() => <ToolMessage row={toolRow(toolCallFixture('execute', { status: 'failed', result: failedResult(marker) }))} />)
    const failedOutputs = failed.container.querySelectorAll('[data-tool-output-preview]')
    expect(failedOutputs).toHaveLength(1)
    expect(failedOutputs[0]?.textContent).toBe(marker)
    expect(failed.container.textContent).toContain('Error')
    const live = render(() => <ToolMessage row={toolRow(toolCallFixture('execute', { status: 'in_progress' }))} progress={{ liveTail: () => ({ outputTail: marker }) }} />)
    const liveOutputs = live.container.querySelectorAll('[data-tool-output-preview]')
    expect(liveOutputs).toHaveLength(1)
    expect(liveOutputs[0]?.textContent).toBe(marker)
  })
})
