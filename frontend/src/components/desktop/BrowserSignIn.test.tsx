import type { SignInOptions } from '~/context/AuthContext'
import { createMemoryHistory, MemoryRouter, Route } from '@solidjs/router'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal, onCleanup } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useAuth } from '~/context/AuthContext'
import { deferred, settle } from '~/test-support/async'
import { browserAuthDriver, DEFAULT_BROWSER_AUTH_START, resetBrowserAuthDriver } from '~/test-support/browserAuthDriver'
import { BrowserSignIn } from './BrowserSignIn'

vi.mock('~/api/platformBridge', async () => {
  const browserAuth = (await import('~/test-support/browserAuthDriver')).browserAuthPlatformMock()
  return {
    ...browserAuth,
    desktopHub: () => ({ kind: 'remote', url: 'https://hub.example' }),
    platformBridge: {
      ...browserAuth.platformBridge,
      switchMode: vi.fn(),
    },
  }
})

const mockAdopt = vi.fn()
// Real signals for the state the adoption moves, so the probes re-render when
// the seam adopts or starts recovery (a plain vi.fn would freeze the first
// answer inside the JSX).
const [currentUser, setCurrentUser] = createSignal<{ id: string } | null>(null)
const [signInRecovery, setSignInRecovery] = createSignal<boolean>(false)
// Held only by the adoption attempt, so the "the wait holds no auth loading"
// assertions read a state the seam can actually raise. A plain value: only the
// tests read it, never a render.
let authLoadingHeld = false

const copyTextToClipboard = vi.hoisted(() => vi.fn<(text: string) => Promise<boolean>>())
vi.mock('~/lib/clipboard', () => ({
  copyTextToClipboard: (text: string) => copyTextToClipboard(text),
}))

vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    user: () => currentUser(),
    loading: () => authLoadingHeld,
    signInRecovery: () => (signInRecovery()
      ? {}
      : null),
    adoptBrowserSignIn: (opts?: SignInOptions) => {
      authLoadingHeld = true
      return Promise.resolve(mockAdopt(opts)).finally(() => {
        authLoadingHeld = false
      })
    },
  }),
}))

function UserProbe() {
  const auth = useAuth()
  return (
    <span data-testid="user-probe">
      {auth.user()
        ? `signed-in:${auth.user()!.id}`
        : 'signed-out'}
    </span>
  )
}

function RecoveryProbe() {
  const auth = useAuth()
  return (
    <span data-testid="recovery-probe">
      {auth.signInRecovery()
        ? 'recovering'
        : 'settled'}
    </span>
  )
}

function SignInHarness() {
  let mounted = true
  onCleanup(() => {
    mounted = false
  })
  return <BrowserSignIn isCurrentArrival={() => mounted} />
}

/**
 * Renders the sign-in at `initialPath` and records every navigation the view
 * causes. The listener attaches after the initial entry is in place, so
 * `navigations` holds exactly those.
 */
function renderSignIn(initialPath = '/login') {
  const history = createMemoryHistory()
  history.set({ value: initialPath, replace: true, scroll: false })
  const navigations: string[] = []
  history.listen(value => navigations.push(value))
  const result = render(() => (
    <>
      <MemoryRouter history={history}>
        <Route path="/login" component={SignInHarness} />
        <Route path="/" component={() => <div data-testid="app-home" />} />
        <Route path="/verify-email" component={() => <div data-testid="verify-email-page" />} />
      </MemoryRouter>
      <UserProbe />
      <RecoveryProbe />
    </>
  ))
  return { ...result, navigations }
}

async function startCeremony() {
  fireEvent.click(await screen.findByRole('button', { name: 'Sign in with your browser' }))
  await vi.waitFor(() => expect(browserAuthDriver.openExternalUrl).toHaveBeenCalled())
}

describe('BrowserSignIn', () => {
  beforeEach(() => {
    resetBrowserAuthDriver()
    copyTextToClipboard.mockReset()
    copyTextToClipboard.mockResolvedValue(true)
    setCurrentUser(null)
    authLoadingHeld = false
    setSignInRecovery(false)
    mockAdopt.mockReset()
    mockAdopt.mockImplementation(async () => {
      setCurrentUser({ id: 'u1' })
      return { outcome: 'adopted', verificationRequired: false }
    })
  })

  it('opens the browser once, adopts the user, and follows the redirect', async () => {
    // `/setup` is a real SPA route, so the client-side branch runs, and its
    // value distinguishes the redirect from the `/` fallback.
    const { navigations } = renderSignIn(`/login?redirect=${encodeURIComponent('/setup?x=1')}`)
    await startCeremony()

    browserAuthDriver.emit({ ceremonyId: 'c-1', ok: true, failure: '' })

    await vi.waitFor(() => expect(navigations).toEqual(['/setup?x=1']))
    expect(browserAuthDriver.openExternalUrl).toHaveBeenCalledExactlyOnceWith('https://hub.example/authorize')
    expect(mockAdopt).toHaveBeenCalledOnce()
  })

  // Whatever the sign-in was asked to continue to, an account that must
  // verify its email goes to /verify-email first.
  it('navigates to /verify-email when the account must verify its email', async () => {
    mockAdopt.mockResolvedValue({ outcome: 'adopted', verificationRequired: true })
    renderSignIn()
    await startCeremony()

    browserAuthDriver.emit({ ceremonyId: 'c-1', ok: true, failure: '' })

    expect(await screen.findByTestId('verify-email-page')).toBeInTheDocument()
  })

  it('shows the reason, returns to the first state, and leaves the user signed out', async () => {
    renderSignIn()
    await startCeremony()

    browserAuthDriver.emit({ ceremonyId: 'c-1', ok: false, failure: 'the code expired' })

    expect(await screen.findByText('the code expired')).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Sign in with your browser' })).toBeInTheDocument()
    expect(mockAdopt).not.toHaveBeenCalled()
    expect(await screen.findByTestId('user-probe')).toHaveTextContent('signed-out')
  })

  // The sidecar's one event decides, so the cancel command itself settles
  // nothing; the first state returns with no reason to show.
  it('cancels the ceremony and returns to the first state with no reason', async () => {
    renderSignIn()
    await startCeremony()

    fireEvent.click(await screen.findByRole('button', { name: 'Cancel' }))
    expect(browserAuthDriver.cancelBrowserAuth).toHaveBeenCalledExactlyOnceWith('c-1')

    browserAuthDriver.emit({ ceremonyId: 'c-1', ok: false, failure: '' })

    expect(await screen.findByRole('button', { name: 'Sign in with your browser' })).toBeInTheDocument()
    // The Hub line and the idle description only: no failure reason renders
    // beside them.
    expect(screen.getAllByRole('paragraph')).toHaveLength(2)
    expect(authLoadingHeld).toBe(false)
    expect(mockAdopt).not.toHaveBeenCalled()
  })

  // A cancel that raced a Hub-accepted code still adopts the session the jar
  // holds, so the user is signed in rather than signed out.
  it('adopts the user when the event of a cancel carries a success', async () => {
    renderSignIn()
    await startCeremony()

    fireEvent.click(await screen.findByRole('button', { name: 'Cancel' }))
    expect(browserAuthDriver.cancelBrowserAuth).toHaveBeenCalledExactlyOnceWith('c-1')

    browserAuthDriver.emit({ ceremonyId: 'c-1', ok: true, failure: '' })

    await vi.waitFor(() => expect(mockAdopt).toHaveBeenCalledOnce())
    expect(await screen.findByTestId('user-probe')).toHaveTextContent('signed-in:u1')
  })

  it('holds no auth loading and adopts nothing while it waits', async () => {
    renderSignIn()
    await startCeremony()

    expect(screen.getByText('Waiting for your browser...')).toBeInTheDocument()
    expect(authLoadingHeld).toBe(false)
    expect(mockAdopt).not.toHaveBeenCalled()
  })

  it('cancels the ceremony when the view unmounts during the wait', async () => {
    const { unmount } = renderSignIn()
    await startCeremony()

    unmount()

    await vi.waitFor(() => expect(browserAuthDriver.cancelBrowserAuth).toHaveBeenCalledExactlyOnceWith('c-1'))
  })

  it('opens no URL and cancels when the view unmounts before the start resolves', async () => {
    const start = deferred<{ ceremonyId: string, authorizeUrl: string }>()
    browserAuthDriver.startBrowserAuth.mockReturnValue(start.promise)
    const { unmount } = renderSignIn()
    fireEvent.click(await screen.findByRole('button', { name: 'Sign in with your browser' }))
    await settle()

    unmount()
    start.resolve(DEFAULT_BROWSER_AUTH_START)

    await vi.waitFor(() => expect(browserAuthDriver.cancelBrowserAuth).toHaveBeenCalledExactlyOnceWith('c-1'))
    expect(browserAuthDriver.openExternalUrl).not.toHaveBeenCalled()
  })

  it('adopts a successful result after unmount without navigating', async () => {
    const { unmount, navigations } = renderSignIn()
    await startCeremony()

    unmount()
    browserAuthDriver.emit({ ceremonyId: 'c-1', ok: true, failure: '' })
    await settle()

    expect(browserAuthDriver.cancelBrowserAuth).toHaveBeenCalledExactlyOnceWith('c-1')
    expect(mockAdopt).toHaveBeenCalledOnce()
    expect(navigations).toEqual([])
  })

  it('disables the wait actions until the flow is ready and after its result arrives', async () => {
    const start = deferred<typeof DEFAULT_BROWSER_AUTH_START>()
    const read = deferred<{ outcome: 'adopted', verificationRequired: boolean }>()
    browserAuthDriver.startBrowserAuth.mockReturnValue(start.promise)
    mockAdopt.mockReturnValue(read.promise)
    renderSignIn()

    fireEvent.click(await screen.findByRole('button', { name: 'Sign in with your browser' }))

    const actions = ['Open again', 'Copy link', 'Cancel'].map(name => screen.getByRole('button', { name }))
    for (const action of actions)
      expect(action).toBeDisabled()

    start.resolve(DEFAULT_BROWSER_AUTH_START)
    await vi.waitFor(() => {
      for (const action of actions)
        expect(action).toBeEnabled()
    })

    browserAuthDriver.emit({ ceremonyId: 'c-1', ok: true, failure: '' })
    await vi.waitFor(() => expect(mockAdopt).toHaveBeenCalledOnce())
    for (const action of actions)
      expect(action).toBeDisabled()

    read.resolve({ outcome: 'adopted', verificationRequired: false })
  })

  it('starts one ceremony when the button is clicked twice during the start', async () => {
    const start = deferred<{ ceremonyId: string, authorizeUrl: string }>()
    browserAuthDriver.startBrowserAuth.mockReturnValue(start.promise)
    renderSignIn()

    const button = await screen.findByRole('button', { name: 'Sign in with your browser' })
    fireEvent.click(button)
    fireEvent.click(button)
    await settle()

    // One view holds one ceremony: the first-state button is gone after the
    // first click (the view swaps it for the waiting actions), and no second
    // ceremony starts.
    expect(screen.queryByRole('button', { name: 'Sign in with your browser' })).toBeNull()
    expect(browserAuthDriver.startBrowserAuth).toHaveBeenCalledTimes(1)
  })

  // The handoff succeeded but the account read failed: recovery owns the
  // state, so this view navigates nowhere and starts nothing more.
  it('does not navigate and starts no second ceremony when the account read fails', async () => {
    mockAdopt.mockImplementation(async () => {
      setSignInRecovery(true)
      return { outcome: 'recovering', verificationRequired: false }
    })
    const { navigations } = renderSignIn()
    await startCeremony()

    browserAuthDriver.emit({ ceremonyId: 'c-1', ok: true, failure: '' })

    await vi.waitFor(() => expect(mockAdopt).toHaveBeenCalledOnce())
    expect(await screen.findByTestId('recovery-probe')).toHaveTextContent('recovering')
    expect(navigations).toEqual([])
    expect(browserAuthDriver.startBrowserAuth).toHaveBeenCalledTimes(1)
  })

  it('opens the same URL again and starts no second ceremony', async () => {
    renderSignIn()
    await startCeremony()

    fireEvent.click(await screen.findByRole('button', { name: 'Open again' }))

    await vi.waitFor(() => expect(browserAuthDriver.openExternalUrl).toHaveBeenCalledTimes(2))
    expect(browserAuthDriver.openExternalUrl).toHaveBeenNthCalledWith(2, 'https://hub.example/authorize')
    expect(browserAuthDriver.startBrowserAuth).toHaveBeenCalledTimes(1)
  })

  it('renders the Hub line with the connected origin', async () => {
    renderSignIn()

    expect(await screen.findByText('Hub: https://hub.example')).toBeInTheDocument()
  })

  it('copies the ceremony link through the clipboard gateway', async () => {
    // A direct `navigator.clipboard` write would reach this spy instead of
    // the gateway, which alone adds the fallback and the failure toast.
    const directWrite = vi.fn()
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText: directWrite },
      configurable: true,
    })
    renderSignIn()
    await startCeremony()

    fireEvent.click(await screen.findByRole('button', { name: 'Copy link' }))

    await vi.waitFor(() => expect(copyTextToClipboard).toHaveBeenCalledExactlyOnceWith('https://hub.example/authorize'))
    expect(directWrite).not.toHaveBeenCalled()
  })
})
