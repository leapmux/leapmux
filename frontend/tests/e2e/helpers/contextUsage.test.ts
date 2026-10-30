import type { Page } from '@playwright/test'
import type { MockModelStep } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { NativeScenarioContext } from './nativeScenario'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { formatTokenCount } from '../../../src/components/chat/rendererUtils'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseContextUsage, expectContextUsage, parseContextRow, SCRIPTED_CONTEXT_USAGE, usageMarkers } from './contextUsage'

/** The browser steps of one scenario in order, and the text that the agent info card states. */
const card = vi.hoisted(() => ({ events: [] as string[], text: '' }))

vi.mock('./ui', async importOriginal => ({
  ...await importOriginal<typeof import('./ui')>(),
  openAgentInfoCard: async () => {
    card.events.push('open-card')
    return { cardProbe: true }
  },
  sendMessage: async (_page: Page, text: string) => {
    card.events.push(`send:${text}`)
  },
  waitForAgentIdle: async () => {
    card.events.push('idle')
  },
}))

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  const check = (value: unknown) => {
    if (typeof value === 'object' && value !== null && 'cardProbe' in value) {
      return {
        toContainText: async (text: string | RegExp) => {
          card.events.push('card-check')
          if (typeof text === 'string')
            expect(card.text).toContain(text)
          else
            expect(card.text).toMatch(text)
        },
      }
    }
    return expect(value)
  }
  return { ...actual, expect: check }
})

/** A context whose model script records each queued step, and whose page records each key and reload. */
function usageContext(fields: Partial<NativeScenarioContext> = {}): NativeScenarioContext & { queued: MockModelStep[] } {
  const queued: MockModelStep[] = []
  const page = Object.assign({} as Page, {
    keyboard: {
      press: async (key: string) => { card.events.push(`key:${key}`) },
    },
    reload: async () => { card.events.push('reload') },
  })
  const modelScript = {
    prompt: (text: string) => `marked:${text}`,
    queue: async (...steps: MockModelStep[]) => {
      queued.push(...steps)
      card.events.push(`queue:${queued.length - steps.length}`)
      return queued.length - steps.length
    },
    waitForSteps: async (count: number) => {
      card.events.push(`steps:${count}`)
    },
  } as unknown as ModelScript
  return {
    page,
    modelScript,
    provider: AgentProvider.CODEX,
    queued,
    ...fields,
  }
}

beforeEach(() => {
  card.events = []
  card.text = `Context ${formatTokenCount(12_040)} / ${formatTokenCount(128_000)}`
})

describe('usageMarkers', () => {
  it.each([
    [
      { inputTokens: 0 },
      1,
    ],
    [
      { outputTokens: 0 },
      1,
    ],
    [
      { cacheReadTokens: 2, cacheWriteTokens: 3 },
      6,
    ],
    [
      { cacheReadTokens: 0 },
      2,
    ],
    [
      { cacheWriteTokens: 0 },
      2,
    ],
  ] as const)('uses the wire defaults for partial counts %j', (usage, total) => {
    expect(usageMarkers(usage)).toEqual([
      formatTokenCount(total),
    ])
  })

  // The marker block every context-usage spec scripts. A default of 1/1 would
  // print one repeated figure and prove nothing about which count moved.
  it('marks the combined figure the card prints', () => {
    expect(usageMarkers({ inputTokens: 12000, outputTokens: 40 })).toEqual([
      formatTokenCount(12040),
    ])
  })

  it('keeps the marker a substring of the card abbreviation of the total', () => {
    for (const [input, output] of [
      [0, 0],
      [1, 1],
      [40, 0],
      [999, 1],
      [1000, 0],
      [12000, 40],
      [250_000, 0],
    ] as const) {
      const [marker] = usageMarkers({ inputTokens: input, outputTokens: output })
      expect(formatTokenCount(input + output), `marker for ${input}+${output}`).toContain(marker!)
    }
  })

  it('returns no marker when the step scripted no count', () => {
    expect(usageMarkers({})).toEqual([])
    expect(usageMarkers({ contextWindow: 200000 })).toEqual([])
  })

  it('still marks when only one count is scripted', () => {
    expect(usageMarkers({ inputTokens: 12000 })).toEqual([
      formatTokenCount(12001),
    ])
    expect(usageMarkers({ outputTokens: 40 })).toEqual([
      formatTokenCount(41),
    ])
  })

  // A cache-inclusive input states the buckets INSIDE it. The card's combined
  // figure sums the normalized breakdown -- ordinary input plus the two buckets
  // plus output -- so a consistent fixture marks input + output.
  it('marks the normalized total of a cache-inclusive usage block', () => {
    expect(usageMarkers({
      inputTokens: 10,
      cacheReadTokens: 2,
      cacheWriteTokens: 3,
      outputTokens: 4,
    }))
      .toEqual([
        formatTokenCount(14),
      ])
  })

  it('clamps an over-full cache breakdown to zero ordinary input', () => {
    expect(usageMarkers({
      inputTokens: 10,
      cacheReadTokens: 8,
      cacheWriteTokens: 5,
      outputTokens: 4,
    }))
      .toEqual([
        formatTokenCount(17),
      ])
  })
})

describe('parseContextRow', () => {
  it('reads the counts that formatTokenCount wrote for the total and the window', () => {
    expect(parseContextRow('AgentCodewhaleSession IDthr_63b7d4cbContext12.2k / 1.0M (1%)')).toEqual({ tokens: 12_200, window: 1_000_000 })
  })

  it('reads a count below one thousand without a unit', () => {
    expect(parseContextRow('Context999 / 200.0k (0%)')).toEqual({ tokens: 999, window: 200_000 })
    expect(parseContextRow('Context0 / 1.0M')).toEqual({ tokens: 0, window: 1_000_000 })
  })

  it('reads the row when the text states a headroom after the percentage', () => {
    expect(parseContextRow('Context45.0k / 200.0k (23% with 16% headroom)')).toEqual({ tokens: 45_000, window: 200_000 })
  })

  it('reads the row of a card whose other rows state digits', () => {
    expect(parseContextRow('Session IDthr_0123456789Working dir/tmp/e-1/2/wdContext3.5k / 128.0k (3%)')).toEqual({ tokens: 3_500, window: 128_000 })
  })

  it('round-trips the output of formatTokenCount to within its rounding', () => {
    for (const total of [
      0,
      1,
      999,
      1_000,
      12_040,
      999_949,
      1_000_000,
      12_500_000,
    ]) {
      const reading = parseContextRow(`Context${formatTokenCount(total)} / ${formatTokenCount(1_000_000)}`)
      const unit = total >= 999_950
        ? 50_000
        : total >= 1_000
          ? 50
          : 0
      expect(Math.abs((reading?.tokens ?? Number.NaN) - total), `total ${total}`).toBeLessThanOrEqual(unit)
    }
  })

  it('returns undefined for a card with no Context row', () => {
    expect(parseContextRow('')).toBeUndefined()
    expect(parseContextRow('AgentCodewhaleSession IDthr_63b7d4cb')).toBeUndefined()
  })

  it('returns undefined for the percentage-only form of the row', () => {
    expect(parseContextRow('Context23% of the context window')).toBeUndefined()
  })
})

describe('usageMarkers native reader', () => {
  it.each([-1, Number.NaN, Number.POSITIVE_INFINITY])('refuses an invalid native count %s', (value) => {
    expect(() => usageMarkers({ inputTokens: 1 }, () => value)).toThrow('invalid token count')
  })

  it('requires a stated usage field before it calls a native reader', () => {
    let calls = 0
    expect(usageMarkers({}, () => {
      calls++
      return 100
    })).toEqual([])
    expect(calls).toBe(0)
  })
})

describe('exerciseContextUsage', () => {
  it('preserves a native zero before and after reload', async () => {
    card.text = 'Context 0 / 128.0k'
    const context = usageContext({
      contextUsageTokens: () => 0,
    })
    await exerciseContextUsage(context, {
      usage: { outputTokens: 40 },
    })
    expect(card.events.filter(event => event === 'card-check')).toHaveLength(2)
  })

  it('uses the native context reader before and after reload', async () => {
    card.text = 'Context 100 / 128.0k'
    const context = usageContext({
      contextUsageTokens: usage => usage.inputTokens ?? 1,
    })
    await exerciseContextUsage(context, {
      usage: { inputTokens: 100, outputTokens: 40 },
    })
    expect(card.events.filter(event => event === 'card-check')).toHaveLength(2)
  })

  it('scripts the usage on one answer, requires the card to state it, and closes the card', async () => {
    const context = usageContext()
    const usage = await exerciseContextUsage(context)
    expect(usage).toEqual(SCRIPTED_CONTEXT_USAGE)
    expect(context.queued).toEqual([
      { text: 'Usage recorded.', usage: SCRIPTED_CONTEXT_USAGE },
    ])
    expect(card.events).toEqual([
      'queue:0',
      'send:marked:Reply once.',
      'steps:1',
      'idle',
      'open-card',
      'card-check',
      'key:Escape',
      'reload',
      'open-card',
      'card-check',
      'key:Escape',
    ])
  })

  it('waits for the step that it queued, after the steps that the script already holds', async () => {
    const context = usageContext()
    context.queued.push({ text: 'An earlier step.' })
    await exerciseContextUsage(context)
    expect(card.events).toContain('steps:2')
  })

  it('answers through the text step of the provider, with the usage on the same step', async () => {
    const context = usageContext({
      textStep: text => ({
        toolCalls: [
          {
            id: 'answer',
            name: 'answer',
            arguments: { text },
          },
        ],
      }),
    })
    await exerciseContextUsage(context)
    expect(context.queued).toEqual([
      {
        toolCalls: [
          {
            id: 'answer',
            name: 'answer',
            arguments: { text: 'Usage recorded.' },
          },
        ],
        usage: SCRIPTED_CONTEXT_USAGE,
      },
    ])
  })

  it('requires the stored usage after reload for every caller', async () => {
    await exerciseContextUsage(usageContext())
    expect(card.events.slice(-4)).toEqual([
      'reload',
      'open-card',
      'card-check',
      'key:Escape',
    ])
  })

  it('uses a distinct later usage block without mutating either input', async () => {
    card.text = 'Context 24.1k / 128.0k'
    const context = usageContext()
    const input = { inputTokens: 24_000, outputTokens: 80, contextWindow: 128_000 }
    const usage = await exerciseContextUsage(context, { usage: input })
    expect(context.queued).toEqual([
      { text: 'Usage recorded.', usage: input },
    ])
    expect(usage).not.toBe(input)
    expect(usage).not.toBe(SCRIPTED_CONTEXT_USAGE)
    expect(card.events.filter(event => event === 'card-check')).toHaveLength(2)
  })

  it('returns a copy that a caller cannot use to change the scripted usage', async () => {
    const usage = await exerciseContextUsage(usageContext())
    usage.inputTokens = 1
    expect(SCRIPTED_CONTEXT_USAGE.inputTokens).toBe(12_000)
  })

  it('fails when the card states another total', async () => {
    card.text = `Context ${formatTokenCount(1)} / ${formatTokenCount(128_000)}`
    // The card must state the scripted input total, 12.0k.
    await expect(exerciseContextUsage(usageContext())).rejects.toMatchObject({
      actual: card.text,
      expected: /Context\s*12\.0k\s*\//,
    })
  })
})

describe('expectContextUsage', () => {
  it.each([
    [
      'Context 100 / 128.0k',
      { inputTokens: 0, outputTokens: 0 },
      /Context\s*0\s*\//,
    ],
    [
      'Context 1000 / 128.0k',
      { inputTokens: 100, outputTokens: 0 },
      /Context\s*100\s*\//,
    ],
  ])('requires the exact context count in %s', async (text, usage, pattern) => {
    card.text = text
    await expect(expectContextUsage(usageContext().page, usage)).rejects.toMatchObject({ actual: text, expected: pattern })
  })

  it('refuses a usage block that states no count, before it opens the card', async () => {
    await expect(expectContextUsage(usageContext().page, { contextWindow: 128_000 })).rejects.toThrow('states a token count')
    expect(card.events).toEqual([])
  })

  it('checks the card for an explicit cache-only usage block', async () => {
    card.text = 'Context 6 / 128.0k'
    await expectContextUsage(usageContext().page, { cacheReadTokens: 2, cacheWriteTokens: 3 })
    expect(card.events).toContain('open-card')
    expect(card.events).toContain('card-check')
  })
})
