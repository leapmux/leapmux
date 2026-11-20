import type { SignInOptions } from '~/context/AuthContext'
import { createMemoryHistory, MemoryRouter, Route } from '@solidjs/router'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useAuth } from '~/context/AuthContext'
import { deferred, settle } from '~/test-support/async'
import { BrowserSignIn } from './BrowserSignIn'

/**
 * The ceremony seams: the sidecar's two commands and its one result event per
 * ceremony. `onEvent` hands the callback the emitted payload, never the Tauri
 * event envelope.
 */
const bridgeMocks = vi.hoisted(() => ({
  openExternalUrl: vi.fn<(url: string) => Promise<void>>(),
  startBrowserAuth: vi.fn<() => Promise<{ ceremonyId: string, authorizeUrl: string }>>(),
  cancelBrowserAuth: vi.fn<(ceremonyId: string) => Promise<void>>(),
  handlers: [] as ((...args: unknown[]) => void)[],
  emit(payload: { ceremonyId: string, ok: boolean, failure: string }): void {
    for (const handler of [...bridgeMocks.handlers])
      handler(payload)
  },
}))

vi.mock('~/api/platformBridge', () => ({
  desktopHub: () => ({ kind: 'remote', url: 'https://hub.example' }),
  openExternalUrl: (url: string) => bridgeMocks.openExternalUrl(url),
  platformBridge: {
    startBrowserAuth: () => bridgeMocks.startBrowserAuth(),
    cancelBrowserAuth: (ceremonyId: string) => bridgeMocks.cancelBrowserAuth(ceremonyId),
    switchMode: vi.fn(),
    onEvent: (_event: string, handler: (...args: unknown[]) => void): Promise<() => void> => {
      bridgeMocks.handlers.push(handler)
      return Promise.resolve(vi.fn(() => {
        const index = bridgeMocks.handlers.indexOf(handler)
        if (index >= 0)
          bridgeMocks.handlers.splice(index, 1)
      }))
    },
  },
}))

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

const START = {
  ceremonyId: 'c-1',
  authorizeUrl: 'https://hub.example/authorize',
}

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
        <Route path="/login" component={() => <BrowserSignIn isCurrentArrival={() => true} />} />
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
  await vi.waitFor(() => expect(bridgeMocks.openExternalUrl).toHaveBeenCalled())
}

describe('BrowserSignIn', () => {
  beforeEach(() => {
    bridgeMocks.openExternalUrl.mockReset()
    bridgeMocks.openExternalUrl.mockResolvedValue(undefined)
    bridgeMocks.startBrowserAuth.mockReset()
    bridgeMocks.startBrowserAuth.mockResolvedValue(START)
    bridgeMocks.cancelBrowserAuth.mockReset()
    bridgeMocks.cancelBrowserAuth.mockResolvedValue(undefined)
    bridgeMocks.handlers.length = 0
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

    bridgeMocks.emit({ ceremonyId: 'c-1', ok: true, failure: '' })

    await vi.waitFor(() => expect(navigations).toEqual(['/setup?x=1']))
    expect(bridgeMocks.openExternalUrl).toHaveBeenCalledExactlyOnceWith('https://hub.example/authorize')
    expect(mockAdopt).toHaveBeenCalledOnce()
  })

  // Whatever the sign-in was asked to continue to, an account that must
  // verify its email goes to /verify-email first.
  it('navigates to /verify-email when the account must verify its email', async () => {
    mockAdopt.mockResolvedValue({ outcome: 'adopted', verificationRequired: true })
    renderSignIn()
    await startCeremony()

    bridgeMocks.emit({ ceremonyId: 'c-1', ok: true, failure: '' })

    expect(await screen.findByTestId('verify-email-page')).toBeInTheDocument()
  })

  it('shows the reason, returns to the first state, and leaves the user signed out', async () => {
    renderSignIn()
    await startCeremony()

    bridgeMocks.emit({ ceremonyId: 'c-1', ok: false, failure: 'the code expired' })

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
    expect(bridgeMocks.cancelBrowserAuth).toHaveBeenCalledExactlyOnceWith('c-1')

    bridgeMocks.emit({ ceremonyId: 'c-1', ok: false, failure: '' })

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
    expect(bridgeMocks.cancelBrowserAuth).toHaveBeenCalledExactlyOnceWith('c-1')

    bridgeMocks.emit({ ceremonyId: 'c-1', ok: true, failure: '' })

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

    await vi.waitFor(() => expect(bridgeMocks.cancelBrowserAuth).toHaveBeenCalledExactlyOnceWith('c-1'))
  })

  it('opens no URL and cancels when the view unmounts before the start resolves', async () => {
    const start = deferred<{ ceremonyId: string, authorizeUrl: string }>()
    bridgeMocks.startBrowserAuth.mockReturnValue(start.promise)
    const { unmount } = renderSignIn()
    fireEvent.click(await screen.findByRole('button', { name: 'Sign in with your browser' }))
    await settle()

    unmount()
    start.resolve(START)

    await vi.waitFor(() => expect(bridgeMocks.cancelBrowserAuth).toHaveBeenCalledExactlyOnceWith('c-1'))
    expect(bridgeMocks.openExternalUrl).not.toHaveBeenCalled()
  })

  it('does not adopt a successful result after the view unmounts', async () => {
    const { unmount } = renderSignIn()
    await startCeremony()

    unmount()
    bridgeMocks.emit({ ceremonyId: 'c-1', ok: true, failure: '' })
    await settle()

    expect(bridgeMocks.cancelBrowserAuth).toHaveBeenCalledExactlyOnceWith('c-1')
    expect(mockAdopt).not.toHaveBeenCalled()
  })

  it('disables the wait actions until the flow is ready and after its result arrives', async () => {
    const start = deferred<typeof START>()
    const read = deferred<{ outcome: 'adopted', verificationRequired: boolean }>()
    bridgeMocks.startBrowserAuth.mockReturnValue(start.promise)
    mockAdopt.mockReturnValue(read.promise)
    renderSignIn()

    fireEvent.click(await screen.findByRole('button', { name: 'Sign in with your browser' }))

    const actions = ['Open again', 'Copy link', 'Cancel'].map(name => screen.getByRole('button', { name }))
    for (const action of actions)
      expect(action).toBeDisabled()

    start.resolve(START)
    await vi.waitFor(() => {
      for (const action of actions)
        expect(action).toBeEnabled()
    })

    bridgeMocks.emit({ ceremonyId: 'c-1', ok: true, failure: '' })
    await vi.waitFor(() => expect(mockAdopt).toHaveBeenCalledOnce())
    for (const action of actions)
      expect(action).toBeDisabled()

    read.resolve({ outcome: 'adopted', verificationRequired: false })
  })

  it('starts one ceremony when the button is clicked twice during the start', async () => {
    const start = deferred<{ ceremonyId: string, authorizeUrl: string }>()
    bridgeMocks.startBrowserAuth.mockReturnValue(start.promise)
    renderSignIn()

    const button = await screen.findByRole('button', { name: 'Sign in with your browser' })
    fireEvent.click(button)
    fireEvent.click(button)
    await settle()

    // One view holds one ceremony: the first-state button is gone after the
    // first click (the view swaps it for the waiting actions), and no second
    // ceremony starts.
    expect(screen.queryByRole('button', { name: 'Sign in with your browser' })).toBeNull()
    expect(bridgeMocks.startBrowserAuth).toHaveBeenCalledTimes(1)
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

    bridgeMocks.emit({ ceremonyId: 'c-1', ok: true, failure: '' })

    await vi.waitFor(() => expect(mockAdopt).toHaveBeenCalledOnce())
    expect(await screen.findByTestId('recovery-probe')).toHaveTextContent('recovering')
    expect(navigations).toEqual([])
    expect(bridgeMocks.startBrowserAuth).toHaveBeenCalledTimes(1)
  })

  it('opens the same URL again and starts no second ceremony', async () => {
    renderSignIn()
    await startCeremony()

    fireEvent.click(await screen.findByRole('button', { name: 'Open again' }))

    await vi.waitFor(() => expect(bridgeMocks.openExternalUrl).toHaveBeenCalledTimes(2))
    expect(bridgeMocks.openExternalUrl).toHaveBeenNthCalledWith(2, 'https://hub.example/authorize')
    expect(bridgeMocks.startBrowserAuth).toHaveBeenCalledTimes(1)
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
