import type { CachedToken } from '~/lib/tokenCache'
import { render } from '@solidjs/testing-library'
import { createComputed, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { shikiHighlighter } from '~/lib/shikiSyncHighlighter'
import { syntaxPairFor } from '~/lib/syntaxThemes'
import { setSyntaxTheme } from '~/lib/syntaxThemeStore'
import { _resetTokenCache, setCachedTokens } from '~/lib/tokenCache'
import { DEFAULT_THEME_ID } from '~/styles/themes'
import { holdWorkerReplies, recordWorkerRequests, settleWorkerReplies } from '~/test-support/fakeWorker'
import { useAsyncCodeTokens } from './useAsyncCodeTokens'

// Every token request goes to the real Shiki worker, which the stand-in Worker
// runs (see ~/test-support/fakeWorker). A test that lands a result AFTER it
// changes the inputs holds the replies of that worker.

/** Hold the replies of the Shiki worker, so that each token request waits until the test releases them. */
function holdTokenReplies() {
  return holdWorkerReplies('lib/shikiWorker.ts')
}

/** The number of token requests that reach the Shiki worker from now on. */
function countTokenRequests(): () => number {
  const requests = recordWorkerRequests('lib/shikiWorker.ts')
  return () => requests.length
}

function Harness(props: { eligible: () => boolean }) {
  const tokens = useAsyncCodeTokens({
    lang: () => 'json',
    code: () => '{"a":1}',
    eligible: () => props.eligible(),
    gate: () => ({ premeasure: false, hold: false }),
  })
  return (
    <div data-testid="state">
      {tokens()
        ? 'TOKENS'
        : 'PLAIN'}
    </div>
  )
}

// Two-source hold gate (scroll-pause OR text-selection), mirroring how ReadResultView /
// the tool bodies compose `hold = syntaxHighlightingPaused || textSelectionActive`.
function HoldHarness(props: { paused: () => boolean, selecting: () => boolean }) {
  const tokens = useAsyncCodeTokens({
    lang: () => 'json',
    code: () => '{"a":1}',
    eligible: () => true,
    gate: () => ({ premeasure: false, hold: props.paused() || props.selecting() }),
  })
  return (
    <div data-testid="state">
      {tokens()
        ? 'TOKENS'
        : 'PLAIN'}
    </div>
  )
}

/** The text that a list of token lines draws. */
function tokenText(tokens: CachedToken[][]): string {
  return tokens.map(line => line.map(token => token.content).join('')).join('\n')
}

/** What one pass of a consumer read: the code, and the text of the tokens, or null for plain. */
interface ConsumerPass {
  code: string
  tokenText: string | null
}

/**
 * Mount the hook over a code that the test changes, beside a computation that
 * reads the code and the tokens together, as a consumer's render does, and
 * record what each pass of that computation reads.
 */
function renderCodeConsumer(initialCode: string) {
  const [code, setCode] = createSignal(initialCode)
  const passes: ConsumerPass[] = []
  render(() => {
    const tokens = useAsyncCodeTokens({
      lang: () => 'json',
      code,
      eligible: () => true,
      gate: () => ({ premeasure: false, hold: false }),
    })
    createComputed(() => {
      const current = tokens()
      passes.push({
        code: code(),
        tokenText: current === null
          ? null
          : tokenText(current),
      })
    })
    return null
  })
  return { setCode, passes }
}

describe('useAsyncCodeTokens', () => {
  beforeEach(() => {
    // No synchronous cache hit: every test takes the asynchronous worker path.
    _resetTokenCache()
  })

  afterEach(async () => {
    // The theme is module state, so put the theme of the file back for the next test.
    await setSyntaxTheme(syntaxPairFor(DEFAULT_THEME_ID), shikiHighlighter)
  })

  // The generation is folded into `currentKey()`, but `on(deps, fn)` tracks the
  // DEPS accessor alone and runs `fn` untracked -- so reading it only inside the
  // body made it invisible to this effect. The theme changed, the caches
  // cleared, the generation bumped, and every applied code surface kept the
  // abandoned theme's baked colours until an unrelated lang, code, gate or
  // eligibility change happened to nudge the effect. This effect is the only
  // thing that redispatches a code surface, so nothing else repainted it.
  it('redispatches when the syntax theme changes', async () => {
    const requests = countTokenRequests()
    const [eligible] = createSignal(true)
    const { getByTestId } = render(() => <Harness eligible={eligible} />)
    await settleWorkerReplies()
    expect(requests()).toBe(1)
    expect(getByTestId('state').textContent).toBe('TOKENS')

    await setSyntaxTheme(syntaxPairFor('nord'), shikiHighlighter)
    await settleWorkerReplies()

    expect(requests()).toBe(2)
  })

  it('drops a late worker result once the body became ineligible (currentKey recheck)', async () => {
    const hold = holdTokenReplies()
    const [eligible, setEligible] = createSignal(true)
    const { getByTestId } = render(() => <Harness eligible={eligible} />)

    // The effect dispatched to the worker, whose reply waits; renders plain meanwhile.
    await settleWorkerReplies()
    expect(hold.pending()).toBe(1)
    expect(getByTestId('state').textContent).toBe('PLAIN')

    // Eligibility flips to false WITHOUT lang/code/gate changing. The main effect
    // re-runs and resets the body to plain, but it does not cancel the dispatch in
    // flight. The currentKey() recheck inside .then() must reject the now-stale
    // result. (Pre-fix this applied the tokens to an ineligible body.)
    setEligible(false)
    await hold.release()
    expect(getByTestId('state').textContent).toBe('PLAIN')
  })

  it('starts highlighting when eligibility flips true with unchanged lang/code', async () => {
    // `eligible` is not lang/code, so a flip must still re-run the effect and dispatch.
    // (Pre-fix the on() deps omitted eligible, so a body that became eligible without a
    // lang/code/gate change never started highlighting until an unrelated change nudged it.)
    const requests = countTokenRequests()
    const [eligible, setEligible] = createSignal(false)
    const { getByTestId } = render(() => <Harness eligible={eligible} />)

    // Ineligible: no worker dispatch, renders plain.
    await settleWorkerReplies()
    expect(requests()).toBe(0)
    expect(getByTestId('state').textContent).toBe('PLAIN')

    // Eligibility flips true with lang/code/gate unchanged -> the effect must re-run + dispatch.
    setEligible(true)
    await settleWorkerReplies()
    expect(requests()).toBe(1)
    expect(getByTestId('state').textContent).toBe('TOKENS')
  })

  it('resets already-applied tokens to plain when eligibility flips false with unchanged lang/code', async () => {
    // The mirror of the above: an already-highlighted body that becomes ineligible (e.g. a
    // size cap now exceeded) must drop back to plain even though lang/code/gate are unchanged.
    const [eligible, setEligible] = createSignal(true)
    const { getByTestId } = render(() => <Harness eligible={eligible} />)

    await settleWorkerReplies()
    expect(getByTestId('state').textContent).toBe('TOKENS')

    setEligible(false)
    await settleWorkerReplies()
    expect(getByTestId('state').textContent).toBe('PLAIN')
  })

  it('applies the worker result when the body is still eligible at resolution', async () => {
    const hold = holdTokenReplies()
    const [eligible] = createSignal(true)
    const { getByTestId } = render(() => <Harness eligible={eligible} />)

    await settleWorkerReplies()
    expect(getByTestId('state').textContent).toBe('PLAIN')
    await hold.release()
    expect(getByTestId('state').textContent).toBe('TOKENS')
  })

  it('stashes a worker result that lands while held and applies it on hold-lift, with no re-dispatch', async () => {
    // Regression: `hold = paused || selecting` re-runs the dispatch effect whenever EITHER
    // source toggles -- even while the combined boolean stays true. The hook must keep the
    // in-flight dispatch live, stash its result, survive held re-runs without losing the
    // stash, and apply it once the hold fully lifts -- all with exactly ONE worker dispatch.
    const requests = countTokenRequests()
    const replies = holdTokenReplies()
    const [paused, setPaused] = createSignal(false)
    const [selecting, setSelecting] = createSignal(false)
    const { getByTestId } = render(() => <HoldHarness paused={paused} selecting={selecting} />)

    // Dispatch while unheld.
    await settleWorkerReplies()
    expect(requests()).toBe(1)
    expect(getByTestId('state').textContent).toBe('PLAIN')

    // A text selection starts (hold on), then the worker resolves WHILE held -> stashed.
    setSelecting(true)
    await replies.release()
    expect(getByTestId('state').textContent).toBe('PLAIN')

    // A scroll-pause flips on then off while the selection stays active: the combined hold
    // stays true throughout, but each toggle re-runs the dispatch effect. The stash must
    // survive these held re-runs without a duplicate dispatch.
    setPaused(true)
    await settleWorkerReplies()
    setPaused(false)
    await settleWorkerReplies()
    expect(getByTestId('state').textContent).toBe('PLAIN')
    expect(requests()).toBe(1)

    // Hold fully lifts -> the stashed result is applied, with no second worker round-trip.
    setSelecting(false)
    await settleWorkerReplies()
    expect(getByTestId('state').textContent).toBe('TOKENS')
    expect(requests()).toBe(1)
  })

  // A token holds its own text, so a consumer that drew the tokens of an earlier
  // code would draw the earlier text. The hook replaces its tokens in an effect,
  // which runs after the computations of the update that changed the code.
  it('answers no tokens of an earlier code to a pass that reads a changed code, while the worker tokenizes it', async () => {
    const { setCode, passes } = renderCodeConsumer('{"a":1}')
    await settleWorkerReplies()
    expect(passes.at(-1)).toEqual({ code: '{"a":1}', tokenText: '{"a":1}' })

    setCode('{"b":2}')
    await settleWorkerReplies()

    expect(passes.at(-1)).toEqual({ code: '{"b":2}', tokenText: '{"b":2}' })
    expect(passes.filter(pass => pass.tokenText !== null && pass.tokenText !== pass.code)).toEqual([])
  })

  it('answers no tokens of an earlier code to a pass that reads a changed code whose tokens the cache holds', async () => {
    const { setCode, passes } = renderCodeConsumer('{"a":1}')
    await settleWorkerReplies()
    expect(passes.at(-1)).toEqual({ code: '{"a":1}', tokenText: '{"a":1}' })
    setCachedTokens('json', '{"b":2}', [
      [
        { content: '{"b":2}' },
      ],
    ])

    setCode('{"b":2}')

    expect(passes.at(-1)).toEqual({ code: '{"b":2}', tokenText: '{"b":2}' })
    expect(passes.filter(pass => pass.tokenText !== null && pass.tokenText !== pass.code)).toEqual([])
  })
})
