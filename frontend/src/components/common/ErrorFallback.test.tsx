/// <reference types="vitest/globals" />
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { createSignal, ErrorBoundary, Show, Suspense } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ErrorFallback, renderErrorFallback } from './ErrorFallback'

const showInfoToast = vi.hoisted(() => vi.fn())
vi.mock('~/components/common/Toast', () => ({ showInfoToast }))

// resolveStack fetches source maps over the network; the fallback must render
// the raw stack until (and if) it resolves.
const resolveStack = vi.hoisted(() => vi.fn(async (stack: string) => `resolved:\n${stack}`))
vi.mock('~/lib/resolveStack', () => ({ resolveStack }))

const copyTextToClipboard = vi.hoisted(() => vi.fn(async (_text: string) => true))
vi.mock('~/lib/clipboard', () => ({ copyTextToClipboard }))

function errorWith(message: string, stack: string) {
  const error = new Error(message)
  error.stack = stack
  return error
}

describe('errorFallback', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    resolveStack.mockImplementation(async (stack: string) => `resolved:\n${stack}`)
    copyTextToClipboard.mockImplementation(async () => true)
  })

  it('renders the message and resolves the stack', async () => {
    render(() => <ErrorFallback error={errorWith('boom', 'at frame.ts:1:1')} reset={() => {}} />)

    expect(screen.getByText('Uncaught Error')).toBeTruthy()
    await waitFor(() => {
      expect(screen.getByRole('alert').textContent).toContain('resolved:')
    })
    expect(screen.getByRole('alert').textContent).toContain('boom')
  })

  it('falls back to the raw stack before resolution completes', () => {
    resolveStack.mockImplementation(() => new Promise<string>(() => {}))
    render(() => <ErrorFallback error={errorWith('boom', 'at frame.ts:1:1')} reset={() => {}} />)

    expect(screen.getByRole('alert').textContent).toContain('at frame.ts:1:1')
  })

  // The last screen standing must not be the one that goes dark. A rejected
  // `createResource` re-throws on every read, so a source-map fetch that failed
  // would take the error UI down with it and leave a blank page -- the error it
  // was mounted to display now unreadable.
  it('still renders when stack resolution rejects', async () => {
    resolveStack.mockRejectedValue(new Error('source map fetch failed'))
    render(() => <ErrorFallback error={errorWith('boom', 'at frame.ts:1:1')} reset={() => {}} />)

    await waitFor(() => expect(resolveStack).toHaveBeenCalled())
    expect(screen.getByRole('alert').textContent).toContain('boom')
    expect(screen.getByRole('alert').textContent).toContain('at frame.ts:1:1')
    expect(screen.getByText('Try again')).toBeTruthy()
  })

  // The route-scoped boundary in `app.tsx` sits inside a `<Suspense>` with no
  // `fallback`, and such a Suspense renders NOTHING while suspended. Reading a
  // pending `createResource` during render registers with it, so a fallback
  // built that way shows a blank page for the length of the source-map fetch --
  // permanently, if the fetch never settles. The raw stack has to paint
  // synchronously, before any of that.
  it('paints synchronously inside a fallback-less Suspense', () => {
    resolveStack.mockImplementation(() => new Promise<string>(() => {}))
    const { container } = render(() => (
      <Suspense>
        <ErrorFallback error={errorWith('boom', 'at frame.ts:1:1')} reset={() => {}} />
      </Suspense>
    ))

    // Synchronously, with no waitFor: a suspended subtree would be empty here.
    expect(container.textContent).toContain('boom')
    expect(container.textContent).toContain('at frame.ts:1:1')
    expect(container.querySelector('[data-testid="error-fallback"]')).toBeTruthy()
  })

  it('survives an error carrying no stack', () => {
    const error = new Error('stackless')
    error.stack = undefined
    render(() => <ErrorFallback error={error} reset={() => {}} />)

    expect(screen.getByRole('alert').textContent).toContain('stackless')
    // Nothing to symbolicate, so nothing should be fetched.
    expect(resolveStack).not.toHaveBeenCalled()
  })

  // `formatErrorMessage` returns '' for a message-less Error -- deliberately, so
  // that other callers render nothing -- and here the raw stack is '' too, so
  // without a placeholder the <pre> holds "\n\n" and the screen's entire payload
  // is gone.
  it('shows a placeholder for an error carrying neither message nor stack', () => {
    const error = new Error('placeholder')
    // Blanked after construction: `unicorn/error-message` forbids writing the
    // `new Error('')` literal, but a handler can genuinely throw one.
    error.message = ''
    error.stack = undefined
    render(() => <ErrorFallback error={error} reset={() => {}} />)

    expect(screen.getByRole('alert').textContent).toContain('Unknown error')
    expect(resolveStack).not.toHaveBeenCalled()
  })

  // Solid types the fallback argument `any` and `throw 'oops'` is legal, so a
  // non-Error throw reaches this component in production.
  it('renders a thrown non-Error value', () => {
    render(() => <ErrorFallback error="just a string" reset={() => {}} />)

    expect(screen.getByRole('alert').textContent).toContain('just a string')
    expect(resolveStack).not.toHaveBeenCalled()
  })

  it('copies the displayed text on click', async () => {
    render(() => <ErrorFallback error={errorWith('boom', 'at frame.ts:1:1')} reset={() => {}} />)
    fireEvent.click(screen.getByText(/boom/))

    await waitFor(() => expect(copyTextToClipboard).toHaveBeenCalled())
    expect(copyTextToClipboard.mock.calls[0][0]).toContain('boom')
    expect(showInfoToast).toHaveBeenCalledWith('Stack trace copied to clipboard')
  })

  // A non-secure origin exposes no clipboard at all. Claiming to have copied
  // when nothing was copied is worse than staying silent, on the one screen
  // where the trace is the only thing the user can carry away.
  it('does not claim success when the clipboard is unavailable', async () => {
    copyTextToClipboard.mockImplementation(async () => false)
    render(() => <ErrorFallback error={errorWith('boom', 'at frame.ts:1:1')} reset={() => {}} />)
    fireEvent.click(screen.getByText(/boom/))

    await waitFor(() => expect(copyTextToClipboard).toHaveBeenCalled())
    expect(showInfoToast).not.toHaveBeenCalled()
  })

  it('invokes reset when "Try again" is pressed', () => {
    const reset = vi.fn()
    render(() => <ErrorFallback error={errorWith('boom', '')} reset={reset} />)

    fireEvent.click(screen.getByText('Try again'))
    expect(reset).toHaveBeenCalledTimes(1)
  })

  describe('as an ErrorBoundary fallback', () => {
    it('re-renders the children when the fault has cleared', () => {
      // Stands in for a transient fault (the HMR race this was built for):
      // the first render throws, the retry succeeds.
      const [broken, setBroken] = createSignal(true)
      const Boom = () => {
        if (broken())
          throw new Error('transient')
        return <span>recovered</span>
      }

      render(() => (
        <ErrorBoundary fallback={renderErrorFallback}>
          <Boom />
        </ErrorBoundary>
      ))
      expect(screen.getByRole('alert').textContent).toContain('transient')

      setBroken(false)
      fireEvent.click(screen.getByText('Try again'))

      expect(screen.getByText('recovered')).toBeTruthy()
      expect(screen.queryByRole('alert')).toBeNull()
    })

    // A deterministic fault must land back on the fallback rather than, say,
    // rendering nothing. Asserting the alert is still present would hold
    // trivially -- it was there before the click -- so this counts renders: an
    // unwired button leaves the ORIGINAL fallback in place and never re-throws.
    it('re-throws into a fresh fallback when the fault persists', () => {
      const renders = vi.fn()
      const Boom = () => {
        renders()
        throw new Error('permanent')
      }

      render(() => (
        <ErrorBoundary fallback={renderErrorFallback}>
          <Boom />
        </ErrorBoundary>
      ))
      expect(renders).toHaveBeenCalledTimes(1)

      fireEvent.click(screen.getByText('Try again'))

      expect(renders).toHaveBeenCalledTimes(2)
      expect(screen.getByRole('alert').textContent).toContain('permanent')
    })

    it('contains the fault so state outside the boundary survives a reset', () => {
      // The reason the router-level boundary exists: an outer provider must not
      // be torn down and re-initialised because a route threw.
      const providerMounts = vi.fn()
      const [broken, setBroken] = createSignal(true)

      const Boom = () => {
        if (broken())
          throw new Error('route fault')
        return <span>route</span>
      }
      const Provider = () => {
        providerMounts()
        return (
          <ErrorBoundary fallback={renderErrorFallback}>
            <Boom />
          </ErrorBoundary>
        )
      }

      render(() => (
        <Show when={true}>
          <Provider />
        </Show>
      ))
      expect(providerMounts).toHaveBeenCalledTimes(1)

      setBroken(false)
      fireEvent.click(screen.getByText('Try again'))

      expect(screen.getByText('route')).toBeTruthy()
      expect(providerMounts).toHaveBeenCalledTimes(1)
    })
  })
})
