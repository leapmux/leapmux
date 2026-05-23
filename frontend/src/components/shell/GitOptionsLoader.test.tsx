/// <reference types="vitest/globals" />
import type { GitInfoFields, GitPathInfo } from '~/hooks/useGitPathInfo'
import { render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { EMPTY_INFO } from '~/hooks/useGitPathInfo'
import { GitOptionsLoader } from './GitOptionsLoader'

// Build a stub matching the GitPathInfo surface the loader reads
// (`loading` + `showGitOptions`). `info` returns the empty snapshot so
// callers don't have to construct a full probe response. An override
// lets specific tests inject an error_hint without re-spelling the
// whole GitInfoFields.
function stubGitInfo(opts: {
  loading: () => boolean
  showGitOptions: () => boolean
  info?: () => GitInfoFields
}): GitPathInfo {
  return {
    loading: opts.loading,
    showGitOptions: opts.showGitOptions,
    info: opts.info ?? (() => EMPTY_INFO),
  }
}

describe('gitOptionsLoader', () => {
  it('renders the spinner while loading is true', () => {
    const gitInfo = stubGitInfo({ loading: () => true, showGitOptions: () => false })
    render(() => <GitOptionsLoader gitInfo={gitInfo}>{() => <div data-testid="body">body</div>}</GitOptionsLoader>)
    expect(screen.getByText(/Loading branch info/)).toBeInTheDocument()
    expect(screen.queryByTestId('body')).toBeNull()
  })

  it('renders the children when loading is false and showGitOptions is true', () => {
    const gitInfo = stubGitInfo({ loading: () => false, showGitOptions: () => true })
    render(() => <GitOptionsLoader gitInfo={gitInfo}>{() => <div data-testid="body">body</div>}</GitOptionsLoader>)
    expect(screen.queryByText(/Loading branch info/)).toBeNull()
    expect(screen.getByTestId('body')).toBeInTheDocument()
  })

  it('renders nothing when loading is false and the path is not a git repo', () => {
    const gitInfo = stubGitInfo({ loading: () => false, showGitOptions: () => false })
    render(() => <GitOptionsLoader gitInfo={gitInfo}>{() => <div data-testid="body">body</div>}</GitOptionsLoader>)
    expect(screen.queryByText(/Loading branch info/)).toBeNull()
    expect(screen.queryByTestId('body')).toBeNull()
  })

  it('does NOT render the children while loading, even if showGitOptions is already true', () => {
    // Edge case: a refresh-in-flight against a known git repo —
    // loading flips back to true while showGitOptions stays true from
    // the previous probe. The loader must defer to the spinner so the
    // children aren't double-mounted against stale data.
    const gitInfo = stubGitInfo({ loading: () => true, showGitOptions: () => true })
    render(() => <GitOptionsLoader gitInfo={gitInfo}>{() => <div data-testid="body">body</div>}</GitOptionsLoader>)
    expect(screen.getByText(/Loading branch info/)).toBeInTheDocument()
    expect(screen.queryByTestId('body')).toBeNull()
  })

  it('renders the error_hint diagnostic when the probe came back with one', () => {
    // dubious-ownership / EACCES path: worker returns IsGitRepo=false
    // (so showGitOptions stays false) but populates error_hint with the
    // underlying git stderr. The loader must surface it inline so the
    // user gets an actionable diagnostic instead of the previous
    // opaque "Internal error" toast.
    const gitInfo = stubGitInfo({
      loading: () => false,
      showGitOptions: () => false,
      info: () => ({
        ...EMPTY_INFO,
        errorHint: 'fatal: detected dubious ownership in repository at /repo',
      }),
    })
    render(() => <GitOptionsLoader gitInfo={gitInfo}>{() => <div data-testid="body">body</div>}</GitOptionsLoader>)
    expect(screen.getByRole('alert')).toHaveTextContent(/dubious ownership/)
    expect(screen.queryByTestId('body')).toBeNull()
  })

  it('does NOT render the error_hint while loading', () => {
    // Edge case: a refresh that comes back with a hint shouldn't double-
    // render the diagnostic underneath the spinner. The spinner is the
    // only thing shown until loading flips false.
    const gitInfo = stubGitInfo({
      loading: () => true,
      showGitOptions: () => false,
      info: () => ({ ...EMPTY_INFO, errorHint: 'permission denied' }),
    })
    render(() => <GitOptionsLoader gitInfo={gitInfo}>{() => <div data-testid="body">body</div>}</GitOptionsLoader>)
    expect(screen.getByText(/Loading branch info/)).toBeInTheDocument()
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('does NOT render the error_hint when showGitOptions is true', () => {
    // Pin the invariant: hint is for the non-git fallback path only.
    // A successful probe (showGitOptions=true) renders the children
    // exclusively.
    const gitInfo = stubGitInfo({
      loading: () => false,
      showGitOptions: () => true,
      info: () => ({ ...EMPTY_INFO, isGitRepo: true, errorHint: 'should-not-render' }),
    })
    render(() => <GitOptionsLoader gitInfo={gitInfo}>{() => <div data-testid="body">body</div>}</GitOptionsLoader>)
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.getByTestId('body')).toBeInTheDocument()
  })

  it('flips from spinner to children when the probe resolves', () => {
    const [loading, setLoading] = createSignal(true)
    const [showOpts, setShowOpts] = createSignal(false)
    const gitInfo = stubGitInfo({ loading, showGitOptions: showOpts })
    render(() => <GitOptionsLoader gitInfo={gitInfo}>{() => <div data-testid="body">body</div>}</GitOptionsLoader>)
    expect(screen.getByText(/Loading branch info/)).toBeInTheDocument()

    setLoading(false)
    setShowOpts(true)

    expect(screen.queryByText(/Loading branch info/)).toBeNull()
    expect(screen.getByTestId('body')).toBeInTheDocument()
  })
})
