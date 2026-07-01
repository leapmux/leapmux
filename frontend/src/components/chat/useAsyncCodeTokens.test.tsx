import type { CachedToken } from '~/lib/tokenCache'
import { render, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useAsyncCodeTokens } from './useAsyncCodeTokens'

// Drive the worker resolution by hand so we can land a result AFTER changing inputs.
let resolveTokenize: ((tokens: CachedToken[][] | null) => void) | undefined
vi.mock('~/lib/shikiWorkerClient', () => ({
  tokenizeAsync: vi.fn(() => new Promise<CachedToken[][] | null>((res) => { resolveTokenize = res })),
}))
vi.mock('~/lib/tokenCache', () => ({
  // No synchronous cache hit: force the async worker path under test.
  getCachedTokens: () => undefined,
  // Real key format (the hook now single-sources it from tokenCache.makeKey).
  makeKey: (lang: string, code: string) => `${lang}\0${code}`,
}))

function Harness(props: { eligible: () => boolean }) {
  const tokens = useAsyncCodeTokens({
    lang: () => 'json',
    code: () => '{"a":1}',
    eligible: () => props.eligible(),
    gate: () => ({ premeasure: false, hold: false }),
  })
  return <div data-testid="state">{tokens() ? 'TOKENS' : 'PLAIN'}</div>
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
  return <div data-testid="state">{tokens() ? 'TOKENS' : 'PLAIN'}</div>
}

describe('useAsyncCodeTokens', () => {
  afterEach(() => {
    resolveTokenize = undefined
    vi.clearAllMocks()
  })

  it('drops a late worker result once the body became ineligible (currentKey recheck)', async () => {
    const [eligible, setEligible] = createSignal(true)
    const { getByTestId } = render(() => <Harness eligible={eligible} />)

    // The effect dispatched to the (mocked, still-pending) worker; renders plain meanwhile.
    await waitFor(() => expect(resolveTokenize).toBeDefined())
    expect(getByTestId('state').textContent).toBe('PLAIN')

    // Eligibility flips to false WITHOUT lang/code/gate changing -- the main effect's
    // on([lang, code, premeasure, hold]) deps do not re-run, so nothing supersedes the
    // in-flight dispatch. The currentKey() recheck inside .then() must reject the now-stale
    // result. (Pre-fix this applied the tokens to an ineligible body.)
    setEligible(false)
    resolveTokenize!([[{ content: '{', htmlStyle: {} }]])
    await Promise.resolve()
    await Promise.resolve()
    expect(getByTestId('state').textContent).toBe('PLAIN')
  })

  it('starts highlighting when eligibility flips true with unchanged lang/code', async () => {
    // `eligible` is not lang/code, so a flip must still re-run the effect and dispatch.
    // (Pre-fix the on() deps omitted eligible, so a body that became eligible without a
    // lang/code/gate change never started highlighting until an unrelated change nudged it.)
    const [eligible, setEligible] = createSignal(false)
    const { getByTestId } = render(() => <Harness eligible={eligible} />)

    // Ineligible: no worker dispatch, renders plain.
    await Promise.resolve()
    await Promise.resolve()
    expect(resolveTokenize).toBeUndefined()
    expect(getByTestId('state').textContent).toBe('PLAIN')

    // Eligibility flips true with lang/code/gate unchanged -> the effect must re-run + dispatch.
    setEligible(true)
    await waitFor(() => expect(resolveTokenize).toBeDefined())
    resolveTokenize!([[{ content: '{', htmlStyle: {} }]])
    await waitFor(() => expect(getByTestId('state').textContent).toBe('TOKENS'))
  })

  it('resets already-applied tokens to plain when eligibility flips false with unchanged lang/code', async () => {
    // The mirror of the above: an already-highlighted body that becomes ineligible (e.g. a
    // size cap now exceeded) must drop back to plain even though lang/code/gate are unchanged.
    const [eligible, setEligible] = createSignal(true)
    const { getByTestId } = render(() => <Harness eligible={eligible} />)

    await waitFor(() => expect(resolveTokenize).toBeDefined())
    resolveTokenize!([[{ content: '{', htmlStyle: {} }]])
    await waitFor(() => expect(getByTestId('state').textContent).toBe('TOKENS'))

    setEligible(false)
    await waitFor(() => expect(getByTestId('state').textContent).toBe('PLAIN'))
  })

  it('applies the worker result when the body is still eligible at resolution', async () => {
    const [eligible] = createSignal(true)
    const { getByTestId } = render(() => <Harness eligible={eligible} />)

    await waitFor(() => expect(resolveTokenize).toBeDefined())
    resolveTokenize!([[{ content: '{', htmlStyle: {} }]])
    await waitFor(() => expect(getByTestId('state').textContent).toBe('TOKENS'))
  })

  it('stashes a worker result that lands while held and applies it on hold-lift, with no re-dispatch', async () => {
    // Regression: `hold = paused || selecting` re-runs the dispatch effect whenever EITHER
    // source toggles -- even while the combined boolean stays true. The hook must keep the
    // in-flight dispatch live, stash its result, survive held re-runs without losing the
    // stash, and apply it once the hold fully lifts -- all with exactly ONE worker dispatch.
    const { tokenizeAsync } = await import('~/lib/shikiWorkerClient')
    const [paused, setPaused] = createSignal(false)
    const [selecting, setSelecting] = createSignal(false)
    const { getByTestId } = render(() => <HoldHarness paused={paused} selecting={selecting} />)

    // Dispatch while unheld.
    await waitFor(() => expect(resolveTokenize).toBeDefined())
    expect(tokenizeAsync).toHaveBeenCalledTimes(1)
    expect(getByTestId('state').textContent).toBe('PLAIN')

    // A text selection starts (hold on), then the worker resolves WHILE held -> stashed.
    setSelecting(true)
    await Promise.resolve()
    resolveTokenize!([[{ content: '{', htmlStyle: {} }]])
    await Promise.resolve()
    await Promise.resolve()
    expect(getByTestId('state').textContent).toBe('PLAIN')

    // A scroll-pause flips on then off while the selection stays active: the combined hold
    // stays true throughout, but each toggle re-runs the dispatch effect. The stash must
    // survive these held re-runs without a duplicate dispatch.
    setPaused(true)
    await Promise.resolve()
    setPaused(false)
    await Promise.resolve()
    expect(getByTestId('state').textContent).toBe('PLAIN')
    expect(tokenizeAsync).toHaveBeenCalledTimes(1)

    // Hold fully lifts -> the stashed result is applied, with no second worker round-trip.
    setSelecting(false)
    await waitFor(() => expect(getByTestId('state').textContent).toBe('TOKENS'))
    expect(tokenizeAsync).toHaveBeenCalledTimes(1)
  })
})
