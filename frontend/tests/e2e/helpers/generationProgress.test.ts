import type { NativeOutputByteProgressCase, NativeTokenProgressCase } from './generationProgress'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { exerciseOutputByteProgress, exerciseTokenProgress, installGenerationObservation, parseGenerationCounters, PROGRESS_OUTPUT_CALL_ID, verifyCompletedGenerationOutput } from './generationProgress'

/** The browser and Worker steps of the scenarios, replaced so that a unit test can follow their order. */
const fakes = vi.hoisted(() => {
  const events: string[] = []
  /** The counter texts that the fake generation probe of the page holds. */
  const samples: string[] = []
  /** A fake chat locator. A check of its visibility records the rows and the text that the scenario filtered on. */
  interface FakeRows {
    fake: string
    filter: (options: { hasText: string }) => FakeRows
    first: () => FakeRows
  }
  const rows = (name: string): FakeRows => ({
    fake: name,
    filter: ({ hasText }) => rows(`${name}:${hasText}`),
    first: () => rows(name),
  })
  const output = {
    command: 'controlled output command',
    firstMarker: 'FIRST42',
    secondMarker: 'SECOND77',
    firstLiveTail: 'first tail',
    secondLiveTail: 'second tail',
    releaseStartOutput: async () => { events.push('release-start-output') },
    waitForFirstOutput: async () => { events.push('wait-first-output') },
    releaseFirstOutput: async () => { events.push('release-first-output') },
    waitForSecondOutput: async () => { events.push('wait-second-output') },
    releaseFinalOutput: async () => { events.push('release-final-output') },
  }
  return { events, samples, rows, output }
})

vi.mock('./nativeScenario', () => ({
  currentNativeAgent: async () => {
    fakes.events.push('agent')
    return { id: 'native-agent', workingDir: '/native/work' }
  },
  nativeTextStep: (_context: unknown, text: string) => ({ text }),
}))
vi.mock('./turnEndSound', () => ({
  observeSettledReceipts: async () => 0,
  waitForIdleSoundReceipt: async () => {
    fakes.events.push('idle-receipt')
  },
}))
vi.mock('./providerToolCalls', () => ({
  bashToolCall: (_provider: unknown, id: string, command: string) => ({ id, name: 'bash', arguments: { command } }),
}))
vi.mock('./toolOutputControl', () => ({ createToolOutputControl: () => fakes.output }))
vi.mock('./ui', () => ({
  answerControl: async (_page: unknown, decision: string) => { fakes.events.push(`answer:${decision}`) },
  assistantBubbles: () => fakes.rows('assistant'),
  messageContents: () => fakes.rows('content'),
  sendMessage: async () => { fakes.events.push('send') },
  waitForAgentIdle: async () => { fakes.events.push('idle') },
  waitForControlBanner: async () => { fakes.events.push('banner') },
}))
// A fake chat locator records its visibility check. Each other value reaches the real assertion.
vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  const fakeExpect = (value: unknown, message?: string) => {
    if (typeof value === 'object' && value !== null && 'fake' in value && typeof value.fake === 'string') {
      const name = value.fake
      return {
        toBeVisible: async () => {
          fakes.events.push(`visible:${name}`)
        },
      }
    }
    return actual.expect(value, message)
  }
  return { ...actual, expect: Object.assign(fakeExpect, { poll: actual.expect.poll }) }
})

beforeEach(() => {
  fakes.events.length = 0
  fakes.samples.length = 0
})

afterEach(() => {
  window.__nativeGenerationProbe?.stop()
  delete window.__nativeGenerationProbe
  document.body.replaceChildren()
  vi.restoreAllMocks()
})

/**
 * A context whose model script queues at `start`. Its page records each evaluate call and each reload, and each
 * evaluate call returns the counter texts of the fake probe.
 */
function fakeContext(start: number, modelScript: Record<string, unknown> = {}): ManagedNativeScenarioContext {
  return {
    page: {
      evaluate: async () => {
        fakes.events.push('evaluate')
        return [...fakes.samples]
      },
      reload: async () => {
        fakes.events.push('reload')
      },
    },
    modelScript: {
      prompt: (text: string) => text,
      queue: async () => {
        fakes.events.push('queue')
        return start
      },
      waitForSteps: async (count: number) => { fakes.events.push(`steps:${count}`) },
      ...modelScript,
    },
  } as unknown as ManagedNativeScenarioContext
}

/** A model script that queues at `start` and records each held and released stream gate without its marker. */
function streamingScript(start: number): Record<string, unknown> {
  const gateName = (gate: string) => gate.replace(/-[^-]+$/, '')
  return {
    queue: async () => {
      fakes.events.push('queue')
      return start
    },
    waitForGate: async (gate: string) => {
      fakes.events.push(`held:${gateName(gate)}`)
    },
    releaseGate: async (gate: string) => {
      fakes.events.push(`released:${gateName(gate)}`)
    },
    releaseGateIfHeld: async () => false,
  }
}

describe('parseGenerationCounters', () => {
  it('reads token and byte labels without treating elapsed time as output', () => {
    expect(parseGenerationCounters('Working for 2s · 123 tokens')).toEqual({ tokens: 123 })
    expect(parseGenerationCounters('Working for 2s · 1.2k tokens · ≥1.5 KB')).toEqual({ tokens: 1200, bytes: 1536 })
    expect(parseGenerationCounters('1.5m tokens · 3 MB')).toEqual({ tokens: 1_500_000, bytes: 3 * 1024 ** 2 })
    expect(parseGenerationCounters('1.2 k tokens')).toEqual({ tokens: 1200 })
    expect(parseGenerationCounters('123     tokens')).toEqual({ tokens: 123 })
    expect(parseGenerationCounters('Working for 300ms')).toEqual({})
  })

  it('keeps zero distinct from an absent counter', () => {
    expect(parseGenerationCounters('1 token')).toEqual({ tokens: 1 })
    expect(parseGenerationCounters('0 tokens · 0 B')).toEqual({ tokens: 0, bytes: 0 })
    expect(parseGenerationCounters('')).toEqual({})
  })

  it('rejects a counter that exceeds the numeric range', () => {
    expect(() => parseGenerationCounters(`${'9'.repeat(400)} tokens`)).toThrow('token counter')
    expect(() => parseGenerationCounters(`${'9'.repeat(400)} MB`)).toThrow('byte counter')
  })
})

describe('installGenerationObservation', () => {
  afterEach(() => {
    window.__nativeGenerationProbe?.stop()
    document.body.replaceChildren()
  })

  /** Add an indicator with the inline style that ThinkingIndicator sets, and start the probe. */
  function indicator(markup: string, style: { display: 'grid' | 'none', rows: '0fr' | '1fr' } = { display: 'grid', rows: '1fr' }): void {
    const element = document.createElement('div')
    element.dataset.testid = 'thinking-indicator'
    element.style.display = style.display
    element.style.gridTemplateRows = style.rows
    element.innerHTML = markup
    const rectangle = new DOMRect(0, 0, 100, 20)
    const rectangles = Object.assign([rectangle], { item: (index: number) => index === 0 ? rectangle : null })
    vi.spyOn(element, 'getClientRects').mockReturnValue(rectangles)
    document.body.append(element)
    installGenerationObservation()
  }

  it('reads current counter values without hidden digit strips or unrelated task text', () => {
    indicator(`
      <span>Task title: read 999 tokens</span>
      <span data-animated-count><span>5 tokens</span><span aria-hidden="true">012345678901234567890123456789</span> tokens</span>
      <span data-animated-count><span>1 KB</span><span aria-hidden="true">012345678901234567890123456789</span> KB</span>
    `)
    expect(window.__nativeGenerationProbe?.samples).toEqual(['5 tokens · 1 KB'])
  })

  it.each([
    { display: 'grid', rows: '0fr' },
    { display: 'none', rows: '1fr' },
  ] as const)('records no counter of an indicator with display $display and rows $rows, which the page does not show', (style) => {
    indicator('<span data-animated-count><span>5 tokens</span></span>', style)
    expect(window.__nativeGenerationProbe?.samples).toEqual([])
  })

  it('ignores token text when the native turn exposes no counter element', () => {
    indicator('<span>Task title: read 777 tokens</span>')
    expect(window.__nativeGenerationProbe?.samples).toEqual([''])
  })
})

describe('verifyCompletedGenerationOutput', () => {
  it('awaits result expansion before checking both exact output markers', async () => {
    const events: string[] = []
    let release!: () => void
    const pending = new Promise<void>((resolve) => {
      release = resolve
    })
    const proof = verifyCompletedGenerationOutput('call_actual_native', { first: 'FIRST42', second: 'SECOND77' }, {
      prepareView: async (callId) => {
        events.push(callId)
        await pending
      },
      assertVisible: async (marker) => { events.push(marker) },
    })
    expect(events).toEqual(['call_actual_native'])
    release()
    await proof
    expect(events).toEqual(['call_actual_native', 'FIRST42', 'SECOND77'])
  })

  it('keeps both marker assertions when the provider needs no expansion', async () => {
    const assertVisible = vi.fn(async (_marker: string) => {})
    await verifyCompletedGenerationOutput('call', { first: 'FIRST42', second: 'SECOND77' }, { assertVisible })
    expect(assertVisible.mock.calls).toEqual([['FIRST42'], ['SECOND77']])
  })

  it('propagates the preparation error before either marker assertion', async () => {
    const failure = new Error('The actual result did not expose its Expand action.')
    const assertVisible = vi.fn(async (_marker: string) => {})
    await expect(verifyCompletedGenerationOutput('call', { first: 'FIRST42', second: 'SECOND77' }, {
      prepareView: async () => { throw failure },
      assertVisible,
    })).rejects.toBe(failure)
    expect(assertVisible).not.toHaveBeenCalled()
  })

  it('propagates a missing second marker instead of accepting the first marker alone', async () => {
    const markers: string[] = []
    const failure = new Error('The second native output marker is absent.')
    await expect(verifyCompletedGenerationOutput('call', { first: 'FIRST42', second: 'SECOND77' }, {
      assertVisible: async (marker) => {
        markers.push(marker)
        if (marker === 'SECOND77')
          throw failure
      },
    })).rejects.toBe(failure)
    expect(markers).toEqual(['FIRST42', 'SECOND77'])
  })

  it.each([{ call: '', first: 'one', second: 'two' }, { call: 'call', first: '', second: 'two' }, { call: 'call', first: 'one', second: '' }, { call: 'call', first: 'same', second: 'same' }])('rejects invalid completed output proof %j', async ({ call, first, second }) => {
    const assertVisible = vi.fn(async (_marker: string) => {})
    await expect(verifyCompletedGenerationOutput(call, { first, second }, { assertVisible })).rejects.toThrow('distinct output markers')
    expect(assertVisible).not.toHaveBeenCalled()
  })
})

describe('exerciseTokenProgress', () => {
  it('runs the preparation before it reads the agent', async () => {
    const failure = new Error('The first stream gate never held.')
    const context = fakeContext(0, {
      waitForGate: async () => {
        throw failure
      },
      releaseGateIfHeld: async () => false,
    })
    const prepare = async () => {
      fakes.events.push('prepare')
    }
    await expect(exerciseTokenProgress(context, { supported: true, prepare })).rejects.toBe(failure)
    expect(fakes.events.slice(0, 2)).toEqual(['prepare', 'agent'])
  })

  it('releases both stream gates and stops the counter probe when the turn fails', async () => {
    const failure = new Error('The first stream gate never held.')
    const released: string[] = []
    const context = fakeContext(0, {
      waitForGate: async () => { throw failure },
      releaseGateIfHeld: async (gate: string) => {
        released.push(gate)
        return false
      },
    })
    await expect(exerciseTokenProgress(context, { supported: false })).rejects.toBe(failure)
    expect(released).toHaveLength(2)
    const [first, second] = released
    expect(first).toMatch(/^progress-first-/)
    expect(second).toBe(first!.replace('progress-first-', 'progress-second-'))
    expect(fakes.events).toEqual(['agent', 'evaluate', 'queue', 'send', 'evaluate'])
  })

  it('ends a turn with no counter, then requires the exact answer marker of the turn again after the reload', async () => {
    await exerciseTokenProgress(fakeContext(5, streamingScript(5)), { supported: false })
    const answer = fakes.events.find(event => event.startsWith('visible:assistant:'))?.slice('visible:assistant:'.length)
    // The marker holds the unique suffix of the turn, so a marker of an earlier turn cannot satisfy the check.
    expect(answer).toMatch(/^NATIVEPROGRESSTEXT[0-9a-f]{32}$/)
    expect(fakes.events).toEqual([
      'agent',
      'evaluate',
      'queue',
      'send',
      'held:progress-first',
      'released:progress-first',
      'held:progress-second',
      'released:progress-second',
      'steps:6',
      `visible:assistant:${answer}`,
      'idle-receipt',
      'idle',
      'evaluate',
      'reload',
      `visible:content:${answer}`,
      'evaluate',
    ])
  })

  it('fails a turn with no counter when the probe saw a counter, before the reload, and stops the probe', async () => {
    fakes.samples.push('5 tokens')
    await expect(exerciseTokenProgress(fakeContext(0, streamingScript(0)), { supported: false })).rejects.toThrow('the whole native turn exposes no live counter')
    expect(fakes.events).not.toContain('reload')
    expect(fakes.events.at(-1)).toBe('evaluate')
  })
})

describe('exerciseOutputByteProgress', () => {
  it('approves the command after the agent requests its step, and releases the held output when the turn fails', async () => {
    const failure = new Error('The first output segment never arrived.')
    vi.spyOn(fakes.output, 'waitForFirstOutput').mockRejectedValueOnce(failure)
    const queued: unknown[][] = []
    const context = fakeContext(3, {
      queue: async (...steps: unknown[]) => {
        queued.push(steps)
        fakes.events.push('queue')
        return 3
      },
    })
    await expect(exerciseOutputByteProgress(context, { supported: true, approveTool: true })).rejects.toBe(failure)
    expect(queued).toEqual([[
      { toolCalls: [{ id: PROGRESS_OUTPUT_CALL_ID, name: 'bash', arguments: { command: fakes.output.command } }] },
      { text: 'The native output scenario completed.' },
    ]])
    expect(fakes.events).toEqual([
      'agent',
      'evaluate',
      'queue',
      'send',
      'steps:4',
      'banner',
      'answer:allow',
      'release-first-output',
      'evaluate',
      'release-final-output',
    ])
  })

  it('releases the start of the output only after the native client started the command', async () => {
    const failure = new Error('The first output segment never arrived.')
    vi.spyOn(fakes.output, 'waitForFirstOutput').mockRejectedValueOnce(failure)
    const context = fakeContext(0)
    const waitForToolStart = async () => {
      fakes.events.push('tool-start')
    }
    await expect(exerciseOutputByteProgress(context, { supported: false, waitForToolStart })).rejects.toBe(failure)
    expect(fakes.events.slice(0, 6)).toEqual(['agent', 'evaluate', 'queue', 'send', 'tool-start', 'release-start-output'])
    expect(fakes.events).not.toContain('banner')
  })

  it('ends a command turn with no counter, then requires both output segments and the answer again after the reload', async () => {
    const boundaries: string[] = []
    await exerciseOutputByteProgress(fakeContext(2), {
      supported: false,
      afterOutputBoundary: async (boundary) => {
        boundaries.push(`${boundary.phase}:${boundary.count}:${boundary.firstMarker}:${boundary.secondMarker}`)
      },
    })
    // A provider with no byte counter gives each boundary the count 0.
    expect(boundaries).toEqual(['first:0:FIRST42:SECOND77', 'second:0:FIRST42:SECOND77'])
    expect(fakes.events).toEqual([
      'agent',
      'evaluate',
      'queue',
      'send',
      'wait-first-output',
      'release-first-output',
      'wait-second-output',
      'release-final-output',
      'steps:4',
      'idle',
      'visible:content:FIRST42',
      'visible:content:SECOND77',
      'idle-receipt',
      'idle',
      'evaluate',
      'reload',
      'visible:content:The native output scenario completed.',
      // The cleanup releases the output again and stops the probe, whether the turn passed or not.
      'release-first-output',
      'evaluate',
      'release-final-output',
    ])
  })
})

describe('NativeTokenProgressCase', () => {
  it('refuses each option of the output byte scenario at compile time', () => {
    const cases: NativeTokenProgressCase[] = [
      { supported: true, step: { reasoning: 'The step thinks first.' } },
      // @ts-expect-error The token scenario runs no tool, so it approves none.
      { supported: true, approveTool: true },
      // @ts-expect-error The token scenario runs no output command.
      { supported: true, outputMarkers: { first: 'FIRST42', second: 'SECOND77' } },
      // @ts-expect-error The token scenario runs no output command.
      { supported: true, waitForToolStart: async () => {} },
      // @ts-expect-error The token scenario runs no output command.
      { supported: true, afterOutputBoundary: async () => {} },
      // @ts-expect-error The token scenario has no tool result.
      { supported: true, prepareCompletedResultView: async () => {} },
    ]
    expect(cases).toHaveLength(6)
  })
})

describe('NativeOutputByteProgressCase', () => {
  it('refuses the answer step of the token scenario at compile time', () => {
    const cases: NativeOutputByteProgressCase[] = [
      { supported: true, approveTool: true },
      // @ts-expect-error The output byte scenario answers with its own step.
      { supported: true, step: { reasoning: 'The step thinks first.' } },
    ]
    expect(cases).toHaveLength(2)
  })
})
