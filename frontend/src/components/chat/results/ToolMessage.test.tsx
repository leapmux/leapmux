import type { ToolCall } from '~/components/chat/model/toolCall'
import type { ToolResultRenderContext } from '~/components/chat/renderContext'
import type { RenderReadinessScope } from '~/components/chat/renderReadiness'
import type { ImageResultSource } from '~/lib/imageBlocks'
import type { ImageDimensions } from '~/lib/imageDimensions'
import type { ToolProgressEntry } from '~/stores/chatToolProgress'
import { fireEvent, render, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MESSAGE_UI_KEY } from '~/components/chat/messageUiKeys'
import { failedResult } from '~/components/chat/model/toolCall'
import { createRenderReadinessScope } from '~/components/chat/renderReadiness'
import { imagesForRow } from '~/components/chat/results/rowImages'
import { ToolMessage } from '~/components/chat/results/ToolMessage'
import { ToolPendingMessage } from '~/components/chat/results/ToolPendingMessage'
import { deferred, settle } from '~/test-support/async'
import { classSelector } from '~/test-support/composedClass'
import { settleWorkerReplies } from '~/test-support/fakeWorker'
import { testImageActions } from '~/test-support/imageActions'
import { toolCallFixture, toolRow } from '~/test-support/toolCallFixture'
import { nativeOutputPathsPrecedePreview } from '../../../../tests/e2e/helpers/nativeToolOutputFilePaths'
import { toolUseTitle, toolUseTitleExpanded } from '../toolStyles.css'
import { toolCallMeta } from './tools/meta'

function progress(outputTail: string | undefined) {
  return (): ToolProgressEntry | undefined =>
    outputTail === undefined
      ? undefined
      : { outputTail }
}

describe('ToolMessage', () => {
  it('renders reported output paths before the native preview without opening a file', () => {
    const call = Object.assign(toolCallFixture('execute', {
      result: {
        commands: [
          { output: 'The original native preview.' },
        ],
        unresolvedTerminals: [],
      },
    }), {
      outputFilePaths: ['/native/tool-output/result.log'],
    })

    const { container } = render(() => <ToolMessage row={toolRow(call)} />)

    expect(container.textContent).toContain('The original native preview.')
    expect(container.textContent).toContain('Output file:')
    expect(container.textContent).toContain('/native/tool-output/result.log')
    expect(container.querySelector('a')).toBeNull()
    expect(container.textContent?.indexOf('/native/tool-output/result.log'))
      .toBeLessThan(container.textContent?.indexOf('The original native preview.') ?? -1)
  })

  it.each([false, true])('keeps command properties before the output when expanded is %s', (expanded) => {
    const output = [
      'PROPERTY_ORDER_PREVIEW',
      ...Array.from({ length: 8 }, (_, index) => `native line ${index}`),
      'PROPERTY_ORDER_LAST_LINE',
    ].join('\n')
    const call = Object.assign(toolCallFixture('execute', {
      request: { command: 'printf native', cwd: '/native/workspace', processId: '4721' },
      result: {
        commands: [
          { output },
        ],
        unresolvedTerminals: [],
      },
      metadata: [
        { label: 'Shell ID', value: 'owned-shell' },
      ],
    }), {
      outputFilePaths: ['/native/output.log'],
    })
    const { container } = render(() => (
      <ToolMessage
        row={toolRow(call)}
        context={{
          getMessageUiState: () => expanded,
        }}
      />
    ))
    if (expanded) {
      expect(container.textContent).toContain('PROPERTY_ORDER_LAST_LINE')
    }
    const text = container.textContent ?? ''
    const preview = text.indexOf('PROPERTY_ORDER_PREVIEW')
    expect(preview).toBeGreaterThanOrEqual(0)
    for (const property of [
      '4721',
      '/native/workspace',
      'owned-shell',
      '/native/output.log',
    ]) {
      expect(text.indexOf(property)).toBeGreaterThanOrEqual(0)
      expect(text.indexOf(property)).toBeLessThan(preview)
    }
    expect(container.querySelectorAll('[data-testid="tool-output-file-paths"]')).toHaveLength(1)
  })

  it('keeps agent properties and output paths before the report', async () => {
    const call = Object.assign(toolCallFixture('agent', {
      result: {
        agents: [
          {
            description: 'Read the module',
            agentId: 'native-child',
            outcome: 'completed',
            statusLabel: 'completed',
            metadata: [
              { label: 'Agent ID', value: 'native-child' },
              { label: 'Model', value: 'native-model' },
            ],
            body: 'PROPERTY_ORDER_AGENT_PREVIEW',
          },
        ],
      },
    }), {
      outputFilePaths: ['/native/agent-report.log'],
    })
    const { container } = render(() => <ToolMessage row={toolRow(call)} />)
    await settleWorkerReplies()
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
      result: {
        commands: [
          { output: 'The native paired preview.' },
        ],
        unresolvedTerminals: [],
      },
    }), {
      outputFilePaths: ['/native/tool-output/paired.log'],
    })

    const request = render(() => <ToolMessage row={toolRow(call, 'request')} />)
    const result = render(() => <ToolMessage row={toolRow(call, 'result', { request: true })} />)

    expect(request.container.textContent).not.toContain('/native/tool-output/paired.log')
    expect(result.container.textContent).toContain('/native/tool-output/paired.log')
    expect(result.container.textContent).toContain('The native paired preview.')
  })

  it('updates output paths when later native metadata reaches the result', () => {
    const original = toolCallFixture('execute', {
      result: {
        commands: [
          { output: 'The original asynchronous preview.' },
        ],
        unresolvedTerminals: [],
      },
    })
    const updated = Object.assign(toolCallFixture('execute', {
      result: {
        commands: [
          { output: 'The original asynchronous preview.' },
        ],
        unresolvedTerminals: [],
      },
    }), {
      outputFilePaths: ['/native/tool-output/completed.log'],
    })
    const [call, setCall] = createSignal<ToolCall>(original)
    const { container } = render(() => <ToolMessage row={toolRow(call())} />)
    expect(container.textContent).not.toContain('/native/tool-output/completed.log')

    setCall(updated)

    expect(container.textContent).toContain('/native/tool-output/completed.log')
    expect(container.textContent).toContain('The original asynchronous preview.')
  })

  it('drops the header on a result row whose request row is beside it', () => {
    const call = toolCallFixture('read', {
      request: { path: '/p/a.ts' },
      result: { lines: null, fallbackContent: 'body' },
    })
    // The layout draws only the body when the request row owns the header.
    // The tool-message attribute belongs to that header's wrapper.
    const paired = render(() => <ToolMessage row={toolRow(call, 'result', { request: true })} />)
    expect(paired.container.querySelector('[data-tool-message]')).toBeNull()
    expect(paired.container.textContent).toContain('body')

    const alone = render(() => <ToolMessage row={toolRow(call, 'result', { request: false })} />)
    expect(alone.container.querySelector('[data-tool-message]')).not.toBeNull()
  })

  // The live output of a call that runs belongs to its PENDING row (ToolPendingMessage).
  // A request row never draws a result, so it draws no live output either: it must not
  // change when the output ends and the result lands.
  it('draws no live output on a request row of a call that has not returned', () => {
    const call = toolCallFixture('execute', {
      status: 'in_progress',
      request: { command: 'make' },
    })
    const liveTail = progress('streaming bytes')
    const { container } = render(() => (
      <ToolMessage
        row={toolRow(call, 'request')}
        context={{
          toolProgress: { liveTail },
        }}
      />
    ))
    expect(container.textContent).toContain('make')
    expect(container.textContent).not.toContain('streaming bytes')
  })

  it('states truncation when the provider kept only part of the output', () => {
    // The completed result carries the call's truncation flag.
    const finished = toolCallFixture('execute', { truncated: true })
    expect(render(() => <ToolMessage row={toolRow(finished)} />).container.textContent).toMatch(/truncated/i)
  })

  it('draws the outcome header above a prose body, and none above a status body', () => {
    const failedFetch = toolCallFixture('fetch', {
      status: 'failed',
      result: failedResult('boom'),
    })
    const fetch = render(() => <ToolMessage row={toolRow(failedFetch)} />)
    expect(fetch.container.textContent).toContain('Error')

    const stoppedTask = toolCallFixture('task', {
      status: 'completed',
      result: { title: 'Stopped task-1', outcome: 'stopped', output: 'The task stopped.' },
    })
    const task = render(() => <ToolMessage row={toolRow(stoppedTask)} />)
    expect(task.container.textContent).toContain('Stopped task-1')
  })

  it('states the outcome when a kind that draws its own outcome drew nothing', () => {
    // These readers use a list or optional value to determine the outcome.
    // Empty values leave no outcome text. The shared header must state the failure.
    const agent = render(() => (
      <ToolMessage row={toolRow(toolCallFixture('agent', {
        status: 'failed',
        result: { agents: [] },
      }))}
      />
    ))
    expect(agent.container.textContent).toContain('Error')

    const execute = render(() => (
      <ToolMessage row={toolRow(toolCallFixture('execute', {
        status: 'failed',
        result: { commands: [], unresolvedTerminals: [] },
      }))}
      />
    ))
    expect(execute.container.textContent).toContain('Error')

    const task = render(() => (
      <ToolMessage row={toolRow(toolCallFixture('task', {
        status: 'failed',
        result: { outcome: 'failed', output: '' },
      }))}
      />
    ))
    expect(task.container.textContent).toContain('Error')
  })

  // An unfinished card shows a neutral icon and the child's status.
  // The shared header must state how the call ended.
  // Codex reports status unavailable when the child's state never arrives.
  it('states the outcome when an agent card has not ended', () => {
    const unknown = {
      description: '',
      agentId: 'thread-1',
      statusLabel: 'status unavailable',
      outcome: 'unknown' as const,
      metadata: [],
      body: '',
    }
    const pending = render(() => (
      <ToolMessage row={toolRow(toolCallFixture('agent', {
        status: 'failed',
        result: {
          agents: [unknown],
        },
      }))}
      />
    ))
    expect(pending.container.textContent).toContain('status unavailable')
    expect(pending.container.textContent).toContain('Error')

    // One unfinished card leaves the row incomplete, so the header still appears.
    const ended = {
      description: '',
      agentId: 'thread-2',
      statusLabel: 'failed',
      outcome: 'failed' as const,
      metadata: [],
      body: 'it broke',
    }
    const mixed = render(() => (
      <ToolMessage row={toolRow(toolCallFixture('agent', {
        status: 'failed',
        result: {
          agents: [ended, unknown],
        },
      }))}
      />
    ))
    expect(mixed.container.textContent).toContain('Error')
  })

  it('keeps the shared outcome header away from a body that states the outcome', async () => {
    const agents = [
      {
        description: 'Fix the build',
        agentId: 'a1',
        statusLabel: 'failed',
        outcome: 'failed' as const,
        metadata: [],
        body: 'it broke',
      },
    ]
    const { container } = render(() => (
      <ToolMessage row={toolRow(toolCallFixture('agent', {
        status: 'failed',
        result: { agents },
      }))}
      />
    ))
    await settleWorkerReplies()
    expect(container.textContent).toContain('it broke')
    expect(container.textContent).not.toContain('Error')
  })

  // A request row draws the same whether or not its call runs, so it shows the prompt
  // only on Expand. The pending row shows the prompt while the subagent runs.
  it('draws the agent prompt on a request row only once the reader expands it', async () => {
    const call = toolCallFixture('agent', {
      status: 'in_progress',
      request: { description: 'Fix the build', prompt: 'Run the tests.' },
    })
    const [expanded, setExpanded] = createSignal<boolean | undefined>(undefined)
    const context: ToolResultRenderContext = {
      getMessageUiState: key => key === MESSAGE_UI_KEY.AGENT_PROMPT
        ? expanded()
        : undefined,
      setMessageUiState: (key, value) => {
        if (key === MESSAGE_UI_KEY.AGENT_PROMPT)
          setExpanded(value)
      },
    }
    const { container } = render(() => <ToolMessage row={toolRow(call, 'request')} context={context} />)
    await settleWorkerReplies()
    expect(container.textContent).toContain('Fix the build')
    expect(container.textContent).not.toContain('Run the tests.')

    setExpanded(true)
    await settleWorkerReplies()
    expect(container.textContent).toContain('Run the tests.')
  })

  // The header of a request row is the newest call of its span: the result row's call
  // once the result landed. The rest of the row stays its own call.
  it('heads a request row with the newer call it is given, and draws its own body', () => {
    const own = toolCallFixture('execute', {
      status: 'in_progress',
      request: { command: 'make all', description: 'Build' },
    })
    const newer = toolCallFixture('execute', {
      request: { command: 'make all', description: 'Build the release' },
      result: {
        commands: [
          { output: 'RESULT OUTPUT' },
        ],
        unresolvedTerminals: [],
      },
    })
    const { container } = render(() => <ToolMessage row={toolRow(own, 'request')} header={newer} />)
    expect(container.textContent).toContain('Build the release')
    expect(container.textContent).toContain('make all')
    expect(container.textContent).not.toContain('RESULT OUTPUT')
  })

  // The header of a request row is one line, so the newer words of its result cannot move
  // the row. Expand shows what the line clips: the row offers it, and it wraps the title.
  describe('a header title that clips', () => {
    /** Make every box report content wider than itself, as a clipped title does. */
    function stubClippedBoxes() {
      vi.spyOn(HTMLElement.prototype, 'scrollWidth', 'get').mockReturnValue(240)
      vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(100)
    }

    afterEach(() => {
      vi.restoreAllMocks()
    })

    const call = toolCallFixture('execute', {
      status: 'in_progress',
      request: { command: 'make' },
    })
    // The words of the Expand button, which the kind states.
    const expandLabel = toolCallMeta(toolRow(call, 'request')).expandLabel ?? 'Expand output'

    it('offers Expand on a row with nothing else to expand, and Expand wraps the title', () => {
      stubClippedBoxes()
      const [expanded, setExpanded] = createSignal(false)
      const { container, getByRole } = render(() => (
        <ToolMessage
          row={toolRow(call, 'request')}
          context={{
            getMessageUiState: key => key === MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED
              ? expanded()
              : undefined,
            setMessageUiState: (key, value) => {
              if (key === MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED)
                setExpanded(value)
            },
          }}
        />
      ))
      expect(toolCallMeta(toolRow(call, 'request')).collapsible).toBe(false)
      const titleBox = () => container.querySelector(classSelector(toolUseTitle))
      expect(titleBox()?.matches(classSelector(toolUseTitleExpanded))).toBe(false)
      fireEvent.click(getByRole('button', { name: expandLabel }))
      expect(titleBox()?.matches(classSelector(toolUseTitleExpanded))).toBe(true)
    })

    it('offers no Expand for a title that fits', () => {
      const { queryByRole } = render(() => <ToolMessage row={toolRow(call, 'request')} />)
      expect(queryByRole('button', { name: expandLabel })).toBeNull()
    })

    // A result row draws its actions in the outer toolbar of the message, which reads
    // the meta alone, so the row reports what its header clips.
    it('reports the clip to the outer toolbar', () => {
      stubClippedBoxes()
      const reports: boolean[] = []
      render(() => (
        <ToolMessage
          row={toolRow(toolCallFixture('execute', {
            request: { command: 'make' },
          }), 'result')}
          context={{
            hasOuterToolbar: true,
            onHeaderClipsChange: clips => reports.push(clips),
          }}
        />
      ))
      expect(reports.at(-1)).toBe(true)
    })
  })

  // The pending row of a call leaves when its result row joins the list, and the request
  // row never draws the preview. A call that ended with NO result -- the turn stopped it
  // -- states the preview on the row that ended it. A result in words answers instead.
  describe('a call that ended with no result', () => {
    const asked = {
      server: 'docs',
      tool: 'lookup',
      args: { query: 'ARGS-MARKER' },
    }

    it('states what it asked on the row that ended it', async () => {
      const call = toolCallFixture('mcp', { status: 'cancelled', request: asked })
      const { container } = render(() => <ToolMessage row={toolRow(call, 'result', { request: true })} />)
      await settleWorkerReplies()
      expect(container.textContent).toContain('ARGS-MARKER')
    })

    it('states no preview beside a result in words', async () => {
      const call = toolCallFixture('mcp', {
        status: 'failed',
        request: asked,
        result: failedResult('the server refused'),
      })
      const { container } = render(() => <ToolMessage row={toolRow(call, 'result', { request: true })} />)
      await settleWorkerReplies()
      expect(container.textContent).toContain('the server refused')
      expect(container.textContent).not.toContain('ARGS-MARKER')
    })

    it('states the carried list of a stopped to-do call', () => {
      const call = toolCallFixture('todo', {
        status: 'cancelled',
        request: {
          items: [
            {
              rowKey: '1',
              content: 'LIST-MARKER',
              status: 'pending',
              activeForm: 'Writing the list',
            },
          ],
        },
      })
      const { container } = render(() => <ToolMessage row={toolRow(call, 'result', { request: true })} />)
      expect(container.textContent).toContain('LIST-MARKER')
    })
  })

  // A result row heads itself with its own call, whatever it is given: it is the newest.
  it('heads a result row with its own call', () => {
    const own = toolCallFixture('execute', {
      request: { command: 'make', description: 'Own words' },
      result: {
        commands: [
          { output: 'done' },
        ],
        unresolvedTerminals: [],
      },
    })
    const other = toolCallFixture('execute', {
      request: { command: 'make', description: 'Other words' },
    })
    const { container } = render(() => <ToolMessage row={toolRow(own, 'result', { request: false })} header={other} />)
    expect(container.textContent).toContain('Own words')
    expect(container.textContent).not.toContain('Other words')
  })

  // Facts that only the result states draw above the result body, on the row that draws
  // the result, and never on the request row.
  it('draws the facts of the result on the result row alone', () => {
    const call = Object.assign(toolCallFixture('execute', {
      result: {
        commands: [
          { output: 'done' },
        ],
        unresolvedTerminals: [],
      },
    }), {
      resultMetadata: [
        { label: 'Plan file', value: '/repo/plan.md' },
      ],
    })
    const request = render(() => <ToolMessage row={toolRow(call, 'request')} />)
    const result = render(() => <ToolMessage row={toolRow(call, 'result', { request: true })} />)
    expect(request.container.textContent).not.toContain('/repo/plan.md')
    expect(result.container.textContent).toContain('Plan file')
    expect(result.container.textContent).toContain('/repo/plan.md')
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

/**
 * Supply image actions so each button reports its index. Without them, the view draws only an image.
 * Clicks each button once every worker reply landed, so the row holds its whole result.
 */
async function drawToolImages(row: ReturnType<typeof toolRow>): Promise<{ container: HTMLElement, opened: number[], drawn: string[] }> {
  const opened: number[] = []
  const context: ToolResultRenderContext = {
    images: testImageActions({
      openImage: request => opened.push(request.index),
    }),
  }
  const { container } = render(() => <ToolMessage row={row} context={context} />)
  await settleWorkerReplies()
  const buttons = [
    ...container.querySelectorAll<HTMLButtonElement>('button[aria-label="Open image"]'),
  ]
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
    extraContent: [
      { type: 'text', text: 'rich extra content' },
      {
        type: 'image',
        source: picture('EXTRA'),
      },
    ],
    images: [
      picture('OWN'),
    ],
    truncated: true,
  })
  const requestRow = toolRow(call, 'request')
  const resultRow = toolRow(call, 'result', { request: true })

  it('draws no result-side content on a request row whose result row is beside it', async () => {
    const { container, opened } = await drawToolImages(requestRow)
    expect(container.textContent).not.toContain('rich extra content')
    expect(container.textContent).not.toContain('the file body')
    expect(container.textContent).not.toMatch(/truncated/i)
    expect(opened).toEqual([])
  })

  it('draws each result-side item exactly once on the result row', async () => {
    const { container, opened } = await drawToolImages(resultRow)
    expect(container.textContent?.match(/rich extra content/g)).toHaveLength(1)
    expect(container.textContent?.match(/the file body/g)).toHaveLength(1)
    expect(opened).toEqual([0, 1])
  })

  it('opens each picture at the index `imagesForRow` reports for the row that drew it', async () => {
    for (const row of [requestRow, resultRow]) {
      const listed = imagesForRow(row)
      const { opened, drawn } = await drawToolImages(row)
      expect(opened).toEqual(listed.map((_source, index) => index))
      expect(drawn).toEqual(listed.map(source => `data:image/png;base64,${source.data}`))
    }
  })

  it('reports only indexes that still address the same picture after a reload', async () => {
    // An image tab stores (seq, index). messageToolResultImages resolves that pair from the completed result.
    // A different index order resolves to another image or no image after reload.
    const afterReload = imagesForRow(toolRow(call, 'result'))
    for (const row of [requestRow, resultRow]) {
      const { opened } = await drawToolImages(row)
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
  it('opens the picture that `imagesForRow` holds at the index it reports', async () => {
    const cases: { call: ToolCall, expected: string[] }[] = [
      {
        call: toolCallFixture('mcp', {
          result: {
            content: [
              { type: 'text', text: 'before' },
              {
                type: 'image',
                source: picture('RESULT'),
              },
            ],
          },
          extraContent: [
            {
              type: 'image',
              source: picture('EXTRA'),
            },
          ],
        }),
        expected: ['RESULT', 'EXTRA'],
      },
      {
        call: toolCallFixture('read', {
          result: { lines: null, fallbackContent: 'the file body' },
          extraContent: [
            {
              type: 'image',
              source: picture('EXTRA'),
            },
          ],
          images: [
            picture('OWN'),
          ],
        }),
        expected: ['EXTRA', 'OWN'],
      },
    ]

    for (const { call, expected } of cases) {
      const row = toolRow(call)
      const listed = imagesForRow(row)
      expect(listed.map(source => source.data)).toEqual(expected)

      const { container, opened, drawn } = await drawToolImages(row)
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
    const call = Object.assign(toolCallFixture('mcp', {
      request: {
        server: '',
        tool: 'codemode',
        args: { code: `text("${marker}")` },
      },
      result: { content: [], structuredJson: metadataJson, structuredJsonRole: 'metadata' },
    }), {
      outputFilePaths: [path],
    })
    const row = toolRow(call)
    const { container } = render(() => (
      <ToolMessage
        row={row}
        context={{
          getMessageUiState: () => true,
        }}
      />
    ))
    expect(container.textContent).toContain(marker)
    expect(container.textContent).toContain(metadataJson)
    expect(container.querySelector('[data-tool-output-preview]')).toBeNull()
    expect(nativeOutputPathsPrecedePreview([container], [marker])).toBe(false)
    expect(toolCallMeta(row).copyableContent()).toBe(metadataJson)
  })

  it.each([false, true])('keeps metadata and paths before actual output when expanded is %s', async (expanded) => {
    const output = [
      marker,
      ...Array.from({ length: 8 }, (_, index) => `native line ${index}`),
      'NATIVE_OUTPUT_OWNER_LAST',
    ].join('\n')
    const call = Object.assign(toolCallFixture('mcp', {
      request: {
        server: '',
        tool: 'codemode',
        args: { code: `text("${marker}")` },
      },
      result: {
        content: [
          { type: 'text', text: output },
        ],
        structuredJson: metadataJson,
        structuredJsonRole: 'metadata',
      },
    }), {
      outputFilePaths: [path],
    })
    const row = toolRow(call)
    const { container } = render(() => (
      <ToolMessage
        row={row}
        context={{
          getMessageUiState: () => expanded,
        }}
      />
    ))
    await settleWorkerReplies()
    const outputs = container.querySelectorAll('[data-tool-output-preview]')
    expect(outputs).toHaveLength(1)
    expect(outputs[0]?.textContent).toContain(marker)
    // The metadata and the path text can hold the marker too, so compare element positions, not text offsets.
    const metadataBlock = [
      ...container.querySelectorAll('div'),
    ].find(element => element.textContent === metadataJson)
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
    const call = Object.assign(toolCallFixture('mcp', {
      request: {
        args: { text: structuredJson },
        server: 'native',
        tool: 'inspect',
      },
      result: { content: [], structuredJson },
    }), {
      outputFilePaths: [path],
    })
    const row = toolRow(call)
    const { container } = render(() => <ToolMessage row={row} />)
    expect(container.querySelectorAll('[data-tool-output-preview]')).toHaveLength(1)
    expect(nativeOutputPathsPrecedePreview([container], ['"count":0'])).toBe(true)
    expect(toolCallMeta(row).copyableContent()).toBe(structuredJson)
  })

  it('keeps output ownership off a paired request row and on its result row', async () => {
    const call = Object.assign(toolCallFixture('mcp', {
      result: {
        content: [
          { type: 'text', text: marker },
        ],
      },
    }), {
      outputFilePaths: [path],
    })
    const request = render(() => <ToolMessage row={toolRow(call, 'request')} />)
    await settleWorkerReplies()
    const result = render(() => <ToolMessage row={toolRow(call, 'result', { request: true })} />)
    await settleWorkerReplies()
    expect(request.container.querySelector('[data-tool-output-preview]')).toBeNull()
    expect(result.container.querySelector('[data-tool-output-preview]')?.textContent).toBe(marker)
    expect(nativeOutputPathsPrecedePreview([result.container], [marker])).toBe(true)
  })

  it('marks failure prose and live native output without marking the outcome header', () => {
    const failed = render(() => (
      <ToolMessage row={toolRow(toolCallFixture('execute', {
        status: 'failed',
        result: failedResult(marker),
      }))}
      />
    ))
    const failedOutputs = failed.container.querySelectorAll('[data-tool-output-preview]')
    expect(failedOutputs).toHaveLength(1)
    expect(failedOutputs[0]?.textContent).toBe(marker)
    expect(failed.container.textContent).toContain('Error')
    const live = render(() => (
      <ToolPendingMessage
        row={toolRow(toolCallFixture('execute', { status: 'in_progress' }), 'request')}
        progress={{
          liveTail: () => ({ outputTail: marker }),
        }}
      />
    ))
    const liveOutputs = live.container.querySelectorAll('[data-tool-output-preview]')
    expect(liveOutputs).toHaveLength(1)
    expect(liveOutputs[0]?.textContent).toBe(marker)
  })
})

/**
 * ToolMessage draws a file image at three places, and each one reads two members of the
 * row's context:
 * - The hold of the display. A held display keeps the picture that a row shows while the
 *   view scrolls or the reader selects text, because a new picture changes the height of
 *   the row.
 * - The readiness scope of the row copy. The image reports there while it does not know
 *   the height of its picture, so no reader measures the copy before the box is reserved.
 */
describe('a file image in a tool row (ToolMessage)', () => {
  const file: ImageResultSource = { filePath: '/held.png' }
  const decoded: ImageResultSource = { mimeType: 'image/png', data: 'iVBORw0KGgo=' }
  const sites: [string, ToolCall][] = [
    [
      'the content of a generic result',
      toolCallFixture('mcp', {
        result: {
          content: [
            { type: 'image', source: file },
          ],
        },
      }),
    ],
    [
      'the extra content of the call',
      toolCallFixture('read', {
        result: { lines: null, fallbackContent: 'the file body' },
        extraContent: [
          { type: 'image', source: file },
        ],
      }),
    ],
    [
      'the images of the call',
      toolCallFixture('read', {
        result: { lines: null, fallbackContent: 'the file body' },
        images: [file],
      }),
    ],
  ]
  // Each hold, with the flag of the render context that states it.
  const holds: [string, 'syntaxHighlightingPaused' | 'textSelectionActive'][] = [
    ['a scroll', 'syntaxHighlightingPaused'],
    ['a text selection', 'textSelectionActive'],
  ]

  it.each(sites.flatMap(([site, call]) => holds.map(([hold, flag]) => [site, hold, call, flag] as const)))(
    'holds a file image in %s during %s, and shows it once the hold ends',
    async (_site, _hold, call, flag) => {
      const [holding, setHolding] = createSignal(true)
      const read = vi.fn(async () => decoded)
      const context: ToolResultRenderContext = {
        ...(flag === 'syntaxHighlightingPaused'
          ? { syntaxHighlightingPaused: holding }
          : { textSelectionActive: holding }),
        images: testImageActions({
          files: { load: read },
        }),
      }
      const { container } = render(() => <ToolMessage row={toolRow(call)} context={context} />)
      await settleWorkerReplies()
      expect(read).toHaveBeenCalledExactlyOnceWith('/held.png')
      // The read landed, and the row still shows what it showed before the read.
      expect(container.querySelector('img')).toBeNull()

      setHolding(false)
      await settle()
      expect(container.querySelector('img')?.getAttribute('src')).toBe(`data:image/png;base64,${decoded.data}`)
    },
  )

  it.each(sites)('holds the row copy while the size read of a file image in %s runs', async (_site, call) => {
    const size = deferred<ImageDimensions | undefined>()
    let scope!: RenderReadinessScope
    render(() => {
      scope = createRenderReadinessScope()
      const context: ToolResultRenderContext = {
        readiness: scope,
        images: testImageActions({
          files: {
            loadSize: () => size.promise,
            peekSize: () => ({ kind: 'unknown' }),
          },
        }),
      }
      return <ToolMessage row={toolRow(call)} context={context} />
    })
    await settleWorkerReplies()
    expect(scope.measurable()).toBe(false)

    size.resolve({ width: 400, height: 200 })

    await waitFor(() => expect(scope.measurable()).toBe(true))
  })
})
