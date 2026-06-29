import type { Accessor } from 'solid-js'
import type { CachedToken } from '~/lib/tokenCache'
import { createEffect, createSignal, on, onCleanup, untrack } from 'solid-js'
import { tokenizeAsync } from '~/lib/shikiWorkerClient'
import { getCachedTokens, makeKey } from '~/lib/tokenCache'

/** Reactive gate controlling whether and when tokens may be (re)computed/applied. */
export interface TokenGate {
  /** Hidden premeasurement: skip tokenization entirely (keep plain geometry). */
  premeasure: boolean
  /**
   * Keep already-applied tokens but DEFER applying any newly-computed ones --
   * visible scroll-pause or an active text selection, where replacing text nodes
   * would clear the selection / start syntax jobs on the scroll-critical path.
   */
  hold: boolean
}

export interface AsyncCodeTokensOptions {
  /** Resolved Shiki language id, or undefined to render plain. Reactive. */
  lang: () => string | undefined
  /** The code text to tokenize. Reactive. */
  code: () => string
  /**
   * Whether the current (reactive) lang/code is eligible for highlighting (size
   * caps, line limits). Resolved from the consumer's own reactive state -- the hook
   * already owns the lang/code being keyed, so this takes no args.
   */
  eligible: () => boolean
  /** Reactive premeasure/hold gate (see {@link TokenGate}). */
  gate: () => TokenGate
  /**
   * Optional synchronous tokenizer for a language the worker can't handle (ANSI,
   * a Shiki main-thread built-in). Return tokens to apply them (terminal, not
   * cached), or null to fall through to the async worker path.
   */
  syncTokenize?: (lang: string, code: string) => CachedToken[][] | null
}

/**
 * Shared reactive token state machine for the chat code surfaces -- the Read tool's
 * line-numbered body and the Bash/JSON tool-result bodies. Returns a signal of the
 * tokenized lines (or null = render plain), recomputing when (lang, code) change,
 * serving the synchronous token cache to avoid a flash on re-expand, dispatching the
 * miss to the tokenize worker, and HOLDING the currently-applied tokens steady while
 * scrolling is paused or a text selection is active (a freshly-computed result is
 * stashed and applied once the hold lifts).
 *
 * The consumers differ only in their inputs -- how they resolve the language, what
 * makes a body eligible, where the premeasure/hold gate comes from, and whether a
 * synchronous main-thread path (ANSI) short-circuits the worker -- so those are the
 * options; the deferral / cancel / cache / dispatch machinery lives here once instead
 * of in two copies that have already drifted.
 */
export function useAsyncCodeTokens(opts: AsyncCodeTokensOptions): Accessor<CachedToken[][] | null> {
  // The key whose tokens are currently APPLIED (an unchanged key never re-dispatches),
  // and tokens computed-but-not-yet-applied because the hold gate was up when they landed.
  let appliedKey: string | undefined
  let pending: { key: string, tokens: CachedToken[][] } | undefined
  // The key of the single in-flight worker dispatch, tracked at the hook level (not per
  // effect-run): a re-run triggered purely by a gate/hold toggle must NOT cancel the
  // dispatch nor start a duplicate -- the same (lang, code) work is still valid, it just
  // gets deferred (stashed) if the hold is still up when it lands.
  let inFlightKey: string | undefined
  // Set once the hook's reactive scope is disposed (component unmount); a worker result
  // that lands afterwards must not write the dead signal.
  let disposed = false
  onCleanup(() => {
    disposed = true
  })

  // The (lang, code) identity key, or undefined when the body is ineligible (no lang,
  // empty, or over a size cap) and should render plain. Single-sources the key both
  // effects compare against. Empty code is never eligible: there is nothing to tokenize,
  // so it must short-circuit here rather than spawn the worker and round-trip an empty
  // string (the per-consumer `eligible` size checks treat 0 chars as within their caps).
  const currentKey = (): string | undefined => {
    const lang = opts.lang()
    const code = opts.code()
    return lang && code.length > 0 && opts.eligible() ? makeKey(lang, code) : undefined
  }

  // Resolve tokens for `code` WITHOUT dispatching the worker: the optional main-thread
  // tokenizer (ANSI) first, then the shared token cache. Reports which source hit, because
  // callers treat the two differently -- a fresh SYNC result is deferred through the hold
  // gate (applyOrStash), a CACHE hit is already-stable text applied immediately. Shared by
  // the seed and the main effect so the two can't drift on which sources are consulted, in
  // what order. null = neither hit; the caller goes async.
  const resolveSyncOrCached = (resolvedLang: string, code: string): { tokens: CachedToken[][], isSync: boolean } | null => {
    const sync = opts.syncTokenize?.(resolvedLang, code) ?? null
    if (sync)
      return { tokens: sync, isSync: true }
    const cached = getCachedTokens(resolvedLang, code)
    if (cached)
      return { tokens: cached, isSync: false }
    return null
  }

  // Seed the signal SYNCHRONOUSLY from the token cache (or the sync ANSI tokenizer) at
  // hook-creation time -- before the first render -- so a warm RE-MOUNT paints highlighted
  // on its first frame instead of flashing the plain `fallback` for a beat. Expanding or
  // collapsing a tool row and switching the diff view (unified<->split) each mount a FRESH
  // hook instance whose `tokens` starts null; the cache read otherwise lives only in the
  // effect below, which Solid runs AFTER the first render commits, so that first frame was
  // always plain even when the tokens were already cached. (Mirrors the markdown path,
  // which reads its cache synchronously during render for exactly this reason --
  // renderMarkdownCachedOrPlain, "already-highlighted rows must not blink back to plain".)
  // Crucially this seeds THROUGH the `hold` gate -- only `premeasure` (a geometry-only
  // render) blocks it. The click that expands/collapses a row (or toggles the diff view)
  // fires a pointerdown, which pauses syntax highlighting for a scroll-idle beat (ChatView's
  // pauseSyntaxHighlightingForScroll), so the freshly-mounted body has hold=true. Unlike the
  // running effects -- which defer applying tokens during hold to avoid SWAPPING text nodes
  // mid-scroll/selection -- the seed is the INITIAL paint of a fresh instance: there is no
  // prior node to swap and no selection on it to clear, so applying the cached tokens
  // immediately is safe, and is exactly what removes the second-view flash. (A cold cache
  // miss still seeds nothing and the worker dispatch is deferred until the hold lifts, so
  // first-time highlighting stays async/off-thread as intended.) Claiming `appliedKey` makes
  // the main effect's first run early-return (keep the seed) rather than reset to plain.
  const seedFromCache = (): CachedToken[][] | null => {
    if (opts.gate().premeasure)
      return null
    const key = currentKey()
    if (!key)
      return null
    // The seed treats a sync result and a cache hit identically -- both are the fresh
    // instance's initial paint (no prior node to swap), so it applies either immediately.
    const resolved = resolveSyncOrCached(opts.lang()!, opts.code())
    if (!resolved)
      return null
    appliedKey = key
    return resolved.tokens
  }

  const [tokens, setTokens] = createSignal<CachedToken[][] | null>(seedFromCache())

  // Drop to plain text and forget any applied/pending tokens. Used wherever the body
  // becomes ineligible or its key changes out from under a not-yet-applied result.
  const resetToPlain = (): void => {
    appliedKey = undefined
    pending = undefined
    setTokens(null)
  }

  // Apply a freshly-computed result for `key`, or stash it when the hold gate is up.
  // Claiming `appliedKey` BEFORE stashing is load-bearing: `hold` re-runs the main
  // effect whenever EITHER hold source toggles (a scroll-pause flip while a text
  // selection stays active), not only when the combined boolean changes, and that
  // re-run's hold branch (`if (appliedKey !== key) resetToPlain()`) would wipe a
  // just-stashed result unless the key is already claimed. The flush effect applies
  // the stash once the hold lifts.
  const applyOrStash = (key: string, next: CachedToken[][]): void => {
    appliedKey = key
    if (untrack(() => opts.gate().hold))
      pending = { key, tokens: next }
    else
      setTokens(next)
  }

  // When the hold gate lifts, apply the tokens that landed while it was up.
  createEffect(() => {
    const gate = opts.gate()
    if (gate.premeasure || gate.hold)
      return
    const key = currentKey()
    if (!key || pending?.key !== key)
      return
    appliedKey = key
    setTokens(pending.tokens)
    pending = undefined
  })

  createEffect(on(
    () => {
      const gate = opts.gate()
      // Track `eligible()` too: it can flip independently of lang/code (a diff side whose
      // own code is byte-identical while the OTHER side grows past the line cap, or a
      // future consumer whose eligibility is driven by external state). Without it in the
      // deps, such a flip would neither reset an over-cap body to plain nor start
      // highlighting a now-eligible one until an unrelated lang/code/gate change nudged it.
      return [opts.lang(), opts.code(), gate.premeasure, gate.hold, opts.eligible()] as const
    },
    ([lang, code, premeasure]) => {
      const key = currentKey()
      if (premeasure || !key) {
        resetToPlain()
        return
      }

      // Held (paused / selecting): keep current tokens only if they still match this
      // key; otherwise drop to plain and let the flush effect apply fresh tokens once
      // the hold lifts. Read the gate fresh (the deps array's value is a snapshot).
      if (untrack(() => opts.gate().hold)) {
        if (appliedKey !== key)
          resetToPlain()
        return
      }

      if (appliedKey === key)
        return

      resetToPlain()

      // key is defined, so lang is defined and eligible.
      const resolvedLang = lang!

      // A synchronous result avoids a worker round-trip and a flash of unstyled text:
      //   - the ANSI main-thread tokenizer (not cached -- ANSI tokens are cheap and
      //     file-specific), applied through applyOrStash so a fresh result defers under hold;
      //   - a token-cache hit for an IN-PLACE key change (code edited / lang resolved on the
      //     same mounted instance), which is already-stable text applied immediately.
      // (The RE-MOUNT case -- expand/collapse, diff view switch -- is handled earlier by
      // seedFromCache, since this effect runs only after the first render.)
      const resolved = resolveSyncOrCached(resolvedLang, code)
      if (resolved) {
        if (resolved.isSync) {
          applyOrStash(key, resolved.tokens)
        }
        else {
          appliedKey = key
          setTokens(resolved.tokens)
        }
        return
      }

      // Async: dispatch to the worker, render plain until it resolves. Dedup by
      // `inFlightKey` so a re-run for the SAME key (a gate/hold toggle, or a
      // held->unheld cycle) does not start a second dispatch -- the live one is still
      // valid and will applyOrStash when it lands. Two guards run on resolution:
      //   - `currentKey() !== key` discards a STALE result -- lang/code/eligible changed
      //     between dispatch and resolution, so the effect re-ran (or is about to) and
      //     superseded this dispatch; and
      //   - `disposed` blocks a write after unmount.
      // Together they replace the old per-run `cancelled` flag, which wrongly discarded
      // an in-flight result on a hold toggle and then re-dispatched the same work after
      // the hold lifted (the documented "stash and apply" never happened for the worker
      // path -- it was masked only because tokenizeAsync caches, so the re-dispatch hit
      // the cache; a cache eviction during the hold re-flashed the body to plain).
      if (inFlightKey === key)
        return
      inFlightKey = key
      tokenizeAsync(resolvedLang, code).then((next) => {
        // Untrack this dispatch only if it is still the live one (a later key change may
        // have superseded inFlightKey with a newer dispatch we must not clear).
        if (inFlightKey === key)
          inFlightKey = undefined
        if (disposed || currentKey() !== key)
          return
        if (!next) {
          setTokens(null)
          return
        }
        applyOrStash(key, next)
      })
    },
  ))

  return tokens
}
