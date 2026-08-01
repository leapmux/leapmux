import type { Component } from 'solid-js'
import { createEffect, createSignal, on } from 'solid-js'
import { showInfoToast } from '~/components/common/Toast'
import { copyTextToClipboard } from '~/lib/clipboard'
import { formatErrorMessage } from '~/lib/errors'
import { resolveStack } from '~/lib/resolveStack'
import * as styles from './ErrorFallback.css'

export interface ErrorFallbackProps {
  /**
   * Typed `unknown` because that is what it is. Solid hands the fallback
   * whatever was thrown -- its own signature says `any` -- and `throw 'oops'`
   * is legal JavaScript, so narrowing to `Error` here would only be a claim the
   * component then has to defend against at runtime anyway.
   */
  error: unknown
  /**
   * Clear the error and re-render the boundary's children. How much of the app
   * that rebuilds is the boundary's choice, not this component's -- see the two
   * boundaries in `app.tsx`.
   */
  reset: () => void
}

/**
 * The presentation for a caught render error, shared by every `ErrorBoundary`
 * so a failure looks and behaves the same wherever it is contained.
 *
 * "Try again" is worth offering even though a deterministic fault lands
 * straight back here: the errors that reach a boundary are not all
 * deterministic. A dev-mode HMR update that re-renders a consumer against a
 * half-patched module graph is the motivating case -- the next render, against
 * the settled graph, succeeds. Before this, the only exit was a page reload,
 * which also threw away the auth session and every bit of shell state.
 */
export const ErrorFallback: Component<ErrorFallbackProps> = (props) => {
  const rawStack = () => (props.error instanceof Error ? props.error.stack : undefined) ?? ''
  const [resolved, setResolved] = createSignal<string>()

  // A plain signal, deliberately NOT `createResource`.
  //
  // Two ways a resource takes this screen down, and it is the one screen whose
  // entire job is to stay up:
  //
  //   - Reading a PENDING resource during render registers with the nearest
  //     enclosing `<Suspense>`. The route-scoped boundary sits inside one that
  //     has no `fallback`, and a suspended Suspense with no fallback renders
  //     NOTHING -- so the error, the stack and "Try again" would all be a blank
  //     page for as long as the source maps take to fetch and parse. The
  //     app-level boundary escaped this only by sitting outside every Suspense.
  //   - Reading a REJECTED resource re-throws, every time, so a source map that
  //     fails to load would throw out of the render below.
  //
  // Neither hazard exists through a signal: the raw stack paints immediately
  // and the symbolicated one swaps in if and when it arrives.
  //
  // The generation counter is what keeps a second error from having its stack
  // overwritten by a slower resolution of the first. Counted rather than
  // re-reading `rawStack()`, so the async callback holds no reactive read.
  let generation = 0
  createEffect(on(rawStack, (stack) => {
    const mine = ++generation
    setResolved(undefined)
    if (!stack)
      return
    void resolveStack(stack).then(
      text => generation === mine && setResolved(text),
      () => {},
    )
  }))

  // `formatErrorMessage` returns `Error.message` verbatim, and an EMPTY one is a
  // deliberate part of its contract (`errors.test.ts` pins it): a handler that
  // throws a message-less Error is saying "no message", which every other caller
  // can render as nothing. This one cannot -- `new Error('')` with no stack would
  // leave the <pre> holding `"\n\n"`, i.e. the boundary's entire payload gone.
  // Substituted here rather than by widening the shared helper.
  const message = () => formatErrorMessage(props.error) || 'Unknown error'
  const displayText = () => `${message()}\n\n${resolved() ?? rawStack()}`

  const handleCopy = async () => {
    if (await copyTextToClipboard(displayText()))
      showInfoToast('Stack trace copied to clipboard')
  }

  return (
    <div class={styles.container} role="alert" data-testid="error-fallback">
      <h1>Uncaught Error</h1>
      <div class={styles.traceColumn}>
        <pre class={styles.trace} onClick={() => void handleCopy()}>
          {displayText()}
        </pre>
        <button type="button" class="outline" onClick={() => props.reset()}>
          Try again
        </button>
      </div>
    </div>
  )
}

/**
 * Adapts Solid's `(error, reset)` fallback signature to {@link ErrorFallback}.
 *
 * Lives here rather than beside each boundary so every one of them shares a
 * single wiring. The app-level boundary spent its whole existence passing a
 * one-argument fallback, which silently dropped `reset` and left the screen
 * with no way out but a page reload.
 */
export function renderErrorFallback(error: unknown, reset: () => void) {
  return <ErrorFallback error={error} reset={reset} />
}
