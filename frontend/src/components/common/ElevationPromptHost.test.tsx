import type { Component } from 'solid-js'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { Code, ConnectError } from '@connectrpc/connect'
import { createMemoryHistory, MemoryRouter, Route } from '@solidjs/router'
import { fireEvent, render, screen, within } from '@solidjs/testing-library'
import { Show } from 'solid-js'
import { Dynamic } from 'solid-js/web'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { elevationInterceptor } from '~/api/transport'
import { Dialog } from '~/components/common/Dialog'
import { ElevationPromptHost } from '~/components/common/ElevationPromptHost'
import { elevationPrompting, promptForElevation, setElevationPrompter } from '~/lib/elevationPrompt'
import { collectFiles, frontendRoot, posixRelative } from '~/test-support/sourceTree'

// The account most of these tests run as: it holds a password, so the form
// offers exactly one factor and the dialog is about the PROMPT rather than
// about which factors an account has.
const PASSWORD_USER = {
  id: 'user-1',
  username: 'alice',
  passwordSet: true,
  passkeyCount: 0,
  oauthProviders: [],
}

const mockUser = vi.fn<() => Record<string, unknown> | null>(() => PASSWORD_USER)
const mockElevateWithPassword = vi.fn()

// The CONTEXT runs the ceremony and adopts the deadline, so this test mocks
// the context at that seam.
vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    user: mockUser,
    elevateWithPassword: (...a: unknown[]) => mockElevateWithPassword(...a),
    elevateWithPasskey: vi.fn(),
  }),
}))

vi.mock('~/lib/systemInfo', async () => {
  const actual = await vi.importActual<typeof import('~/lib/systemInfo')>('~/lib/systemInfo')
  return { ...actual, passkeyBlocker: () => 'origin-not-allowed' as const, passkeysUsableHere: () => false }
})

/**
 * The host reads the address, because the OAuth option needs one to return the
 * browser to, so every case mounts it under a Router.
 *
 * `content` is what sits BENEATH the prompt. The stacking cases pass an open
 * dialog there; every other case passes nothing, which is the surface that
 * raised a restricted call from a plain button.
 */
function renderHost(at = '/', content?: Component) {
  const history = createMemoryHistory()
  if (at !== '/')
    history.set({ value: at, replace: true })
  const Page: Component = () => (
    <>
      <Show when={content}>{surface => <Dynamic component={surface()} />}</Show>
      <ElevationPromptHost />
    </>
  )
  return render(() => (
    <MemoryRouter history={history}>
      <Route path="/" component={Page} />
    </MemoryRouter>
  ))
}

/** The prompt's own dialog, so a query cannot pick up the one beneath it. */
function promptDialog() {
  return screen.getByRole('dialog', { name: 'Verify your identity' })
}

function dismissPrompt() {
  fireEvent.click(within(promptDialog()).getByRole('button', { name: /close/i }))
}

describe('elevationPromptHost', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    // RE-ESTABLISHED per test, not left to the factory above: a test that
    // needs a different account calls mockReturnValue, and that override
    // outlives clearAllMocks -- which clears the recorded calls alone.
    mockUser.mockReturnValue(PASSWORD_USER)
    mockElevateWithPassword.mockResolvedValue(undefined)
    HTMLDialogElement.prototype.showModal = vi.fn(function (this: HTMLDialogElement) {
      this.open = true
    })
    HTMLDialogElement.prototype.close = vi.fn(function (this: HTMLDialogElement) {
      this.open = false
    })
  })
  afterEach(() => setElevationPrompter(null))

  // Nothing is on screen until the TRANSPORT asks. Attempt-then-prompt: a
  // client that decided for itself would either prompt for a window the user
  // already holds or let them fill in a form the hub is about to refuse.
  it('shows nothing until the transport asks for a factor', () => {
    renderHost()
    expect(screen.queryByRole('dialog', { name: 'Verify your identity' })).not.toBeInTheDocument()
  })

  it('opens the prompt and resolves true once a factor is proven', async () => {
    renderHost()

    const answered = promptForElevation()
    expect(await screen.findByRole('dialog', { name: 'Verify your identity' })).toBeInTheDocument()
    expect(elevationPrompting()).toBe(true)

    fireEvent.input(screen.getByTestId('elevate-password'), { target: { value: 'secret' } })
    fireEvent.click(screen.getByTestId('elevate-password-submit'))

    await expect(answered).resolves.toBe(true)
    expect(mockElevateWithPassword).toHaveBeenCalledWith('secret')
    expect(elevationPrompting()).toBe(false)
  })

  /**
   * The account that has no other option: no password, no passkey, one
   * provider. It elevates ONLY through that provider, and that leg is a
   * full-document navigation out of the app.
   */
  const PROVIDER_ONLY_USER = {
    id: 'user-1',
    username: 'alice',
    passwordSet: false,
    passkeyCount: 0,
    oauthProviders: [{ id: 'github-1', name: 'GitHub', enabled: true }],
    mayElevateThroughAProvider: true,
  }

  // The OAuth leg used to send the browser to "/" and drop the panel with it,
  // because Preferences had no address. It has one now, so the leg returns the
  // user to the panel they were in.
  it('returns the browser to the address it left', async () => {
    mockUser.mockReturnValue(PROVIDER_ONLY_USER)
    renderHost('/?prefs=account')

    const answered = promptForElevation()
    const link = await screen.findByTestId('elevate-oauth-github-1')

    const redirect = new URL(link.getAttribute('href')!, 'https://hub.test').searchParams.get('redirect')
    expect(redirect).toBe('/?prefs=account')
    expect(redirect).not.toBe('/')

    dismissPrompt()
    await expect(answered).resolves.toBe(false)
  })

  // The provider option costs the user nothing to read about now. It returns
  // the browser to the address it left, and the one surface that composes text
  // behind this prompt -- the account email field -- restores what was typed.
  // A warning about a loss that does not happen is noise on a screen that is
  // asking for a password.
  it('warns about no cost, because the round trip has none', async () => {
    mockUser.mockReturnValue(PROVIDER_ONLY_USER)
    renderHost('/?prefs=account')

    const answered = promptForElevation()
    await vi.waitFor(() => expect(screen.getByTestId('elevate-oauth-github-1')).toBeInTheDocument())
    expect(screen.queryByTestId('elevate-oauth-discards-typed-text')).toBeNull()
    expect(screen.queryByText(/what you typed/i)).toBeNull()
    expect(screen.queryByText(/home page/i)).toBeNull()

    dismissPrompt()
    await expect(answered).resolves.toBe(false)
  })

  // Dismissing REPORTS the cancellation. Silence would leave the page looking
  // exactly as it did before the click, with the new password still typed in
  // and nothing to say that the app did not save it.
  it('resolves false when the prompt is dismissed', async () => {
    renderHost()

    const answered = promptForElevation()
    await screen.findByRole('dialog', { name: 'Verify your identity' })
    dismissPrompt()

    await expect(answered).resolves.toBe(false)
    expect(elevationPrompting()).toBe(false)
  })

  // Two requests refused at the same instant must not stack two dialogs. The
  // gate this replaced needed a provider to prevent exactly that, and
  // proving a factor in the top one dropped the other's action.
  it('shares one prompt across concurrent refusals', async () => {
    renderHost()

    const first = promptForElevation()
    const second = promptForElevation()
    await screen.findByRole('dialog', { name: 'Verify your identity' })
    expect(screen.getAllByRole('dialog', { name: 'Verify your identity' })).toHaveLength(1)

    fireEvent.input(screen.getByTestId('elevate-password'), { target: { value: 'secret' } })
    fireEvent.click(screen.getByTestId('elevate-password-submit'))

    await expect(first).resolves.toBe(true)
    await expect(second).resolves.toBe(true)
    expect(mockElevateWithPassword).toHaveBeenCalledTimes(1)
  })

  /**
   * THE HOST OWNS THE STACK.
   *
   * A restricted call raised from inside an already-open dialog puts this
   * prompt on top of it, and this component is the layer that knows so.
   * `Dialog` uses the native modal `<dialog>`, so the browser stacks the
   * newest one highest and inerts the rest; the host states the same thing on
   * the elements, which is what lets a surface stop managing the stack for
   * itself.
   *
   * The Account panel used to pre-empt it per click: it read the mirrored
   * elevation deadline and opened the prompt BEFORE its own dialog. That
   * decided no authorization -- the interceptor runs on the request either
   * way -- and it left the next dialog with a restricted call to copy the
   * same reasoning.
   */
  const SurfaceDialog: Component = () => (
    <Dialog title="Add passkey" onClose={() => {}}>
      <button type="button" data-testid="surface-continue">Continue</button>
    </Dialog>
  )

  it('opens exactly one prompt above the dialog that raised the call', async () => {
    renderHost('/', SurfaceDialog)
    const surface = screen.getByRole('dialog', { name: 'Add passkey' })

    const answered = promptForElevation()

    await screen.findByRole('dialog', { name: 'Verify your identity' })
    expect(screen.getAllByRole('dialog', { name: 'Verify your identity' })).toHaveLength(1)
    // The dialog beneath stays mounted, so what the user typed into it
    // survives the prompt.
    expect(surface).toBeInTheDocument()

    dismissPrompt()
    await expect(answered).resolves.toBe(false)
  })

  it('holds the dialog beneath inert until the prompt settles', async () => {
    renderHost('/', SurfaceDialog)
    const surface = screen.getByRole('dialog', { name: 'Add passkey' })
    expect(surface).not.toHaveAttribute('inert')

    const answered = promptForElevation()

    await screen.findByRole('dialog', { name: 'Verify your identity' })
    expect(surface).toHaveAttribute('inert')

    dismissPrompt()
    await expect(answered).resolves.toBe(false)
    expect(surface).not.toHaveAttribute('inert')
  })

  it('returns focus where the prompt found it', async () => {
    renderHost('/', SurfaceDialog)
    const trigger = screen.getByTestId('surface-continue')
    trigger.focus()
    expect(document.activeElement).toBe(trigger)

    const answered = promptForElevation()
    await screen.findByRole('dialog', { name: 'Verify your identity' })
    fireEvent.input(screen.getByTestId('elevate-password'), { target: { value: 'secret' } })
    fireEvent.click(screen.getByTestId('elevate-password-submit'))

    await expect(answered).resolves.toBe(true)
    expect(document.activeElement).toBe(trigger)
  })

  // The release goes with the settle. A host disposed mid-prompt that
  // unregistered alone would leave the dialog beneath inert with nothing left
  // to release it, and the panel would be dead for the life of the page.
  it('releases the dialog beneath when it is disposed mid-prompt', async () => {
    const { unmount } = renderHost('/', SurfaceDialog)
    const surface = screen.getByRole('dialog', { name: 'Add passkey' })
    const abandoned = promptForElevation()
    await screen.findByRole('dialog', { name: 'Verify your identity' })
    expect(surface).toHaveAttribute('inert')

    unmount()

    await expect(abandoned).resolves.toBe(false)
    expect(surface).not.toHaveAttribute('inert')
  })

  // The two cases below drive the REAL interceptor against the REAL dialog.
  // Every other case in this file, and every case in `~/api/transport.test.ts`,
  // substitutes one half: those register a fake prompter, and the ones above
  // call `promptForElevation` directly. Neither half can catch a break in the
  // composition, and the composition is the whole feature -- a refused request
  // reaching a prompt with no call site involved is what replaced the per-call
  // `gate.run(...)` opt-in.
  //
  // `next` counts its calls, so the retry is proven by the call count rather
  // than by the resolved value alone. A stub that ignored the refusal and
  // returned 'ok' immediately would pass on the value.
  function elevationRequired(): ConnectError {
    const meta = new Headers()
    meta.set('leapmux-elevation-required', '1')
    return new ConnectError('this action needs a recent sign-in', Code.FailedPrecondition, meta)
  }

  /**
   * A request in the shape Connect hands an interceptor.
   *
   * The deadline signal and the `connect-timeout-ms` header are not
   * decoration: the retry reads both, because it restarts the call's budget
   * rather than spending what the prompt already consumed. See the retry
   * deadline cases in `~/api/transport.test.ts`.
   */
  function unaryReq() {
    const header = new Headers()
    header.set('connect-timeout-ms', '30000')
    return { stream: false, signal: new AbortController().signal, header } as never
  }

  it('opens the dialog from a refused request and retries it', async () => {
    renderHost()

    let calls = 0
    const next = vi.fn(async () => {
      calls++
      if (calls === 1)
        throw elevationRequired()
      return 'ok' as never
    })
    const answered = elevationInterceptor(next)(unaryReq())

    fireEvent.input(await screen.findByTestId('elevate-password'), { target: { value: 'secret' } })
    fireEvent.click(screen.getByTestId('elevate-password-submit'))

    await expect(answered).resolves.toBe('ok')
    expect(calls).toBe(2)
    expect(mockElevateWithPassword).toHaveBeenCalledWith('secret')
  })

  // Dismissing rethrows the HUB's refusal, which every surface already renders.
  // The request must not be retried: the user declined to prove a factor, so
  // the hub refuses a second attempt identically.
  it('rethrows the hub refusal when the dialog is dismissed', async () => {
    renderHost()

    const next = vi.fn(async () => {
      throw elevationRequired()
    })
    const answered = elevationInterceptor(next)(unaryReq())

    await screen.findByRole('dialog', { name: 'Verify your identity' })
    dismissPrompt()

    await expect(answered).rejects.toThrow('recent sign-in')
    expect(next).toHaveBeenCalledTimes(1)
  })

  // Unmounted, a refusal must rethrow the hub's message rather than await a
  // promise nothing can settle.
  it('unregisters on unmount', async () => {
    const { unmount } = renderHost()
    unmount()
    await expect(promptForElevation()).resolves.toBe(false)
  })

  // Unregistration alone is not enough, and this host really does go away under
  // a running app: the app-root ErrorBoundary unmounts its subtree when it
  // catches, and the desktop launcher unmounts the connected subtree when the
  // connection drops. An open prompt that stays pending leaves `inFlight` and
  // `prompting` set for the life of the page, and the next host to mount
  // registers again, so the "nobody can prompt" branch stops shielding: every
  // later refusal receives the DEAD promise and hangs with no error.
  it('settles an open prompt when it unmounts', async () => {
    const { unmount } = renderHost()
    const abandoned = promptForElevation()
    await screen.findByRole('dialog', { name: 'Verify your identity' })

    unmount()

    // A race rather than a bare await: an unsettled promise is the defect, and
    // this reports it as a value instead of as a test timeout. The correct code
    // settles on a microtask, far inside the delay.
    const pending = Symbol('pending')
    const answer = await Promise.race([
      abandoned,
      new Promise(done => setTimeout(done, 50, pending)),
    ])
    expect(
      answer,
      'A disposed host must settle the prompt it still holds. Unregistering '
      + 'alone leaves that promise pending for ever.',
    ).toBe(false)
    expect(elevationPrompting()).toBe(false)
  })

  // The consequence of the case above, driven end to end: the next host must
  // get a working prompt rather than the dead one.
  it('serves a fresh prompt after a host is disposed mid-prompt', async () => {
    const first = renderHost()
    const abandoned = promptForElevation()
    await screen.findByRole('dialog', { name: 'Verify your identity' })
    first.unmount()
    await abandoned

    renderHost()
    const answered = promptForElevation()
    fireEvent.input(await screen.findByTestId('elevate-password'), { target: { value: 'secret' } })
    fireEvent.click(screen.getByTestId('elevate-password-submit'))

    await expect(answered).resolves.toBe(true)
  })

  // Where it mounts is the whole behaviour, so this test pins it rather than
  // leaving it to a reviewer. The transport refuses on an RPC from ANY surface,
  // so a host mounted inside one panel answers only that panel's refusals, and
  // every other surface shows the hub's raw refusal text with no prompt and no
  // way to continue. That is why this component left `components/settings`. It
  // is a latent failure rather than a live one -- the Account panel issues
  // every call that requires elevation today -- and a latent failure is exactly
  // what a guard is for: the call that exposes it would be added somewhere else
  // entirely, by someone with no reason to look here.
  //
  // A source scan rather than a render: mounting is a fact about the app tree,
  // and rendering the real root here would need the whole provider stack, the
  // router and the platform bridge.
  //
  // It matches an IMPORT of the module or a JSX mount, rather than the bare
  // name. A plain substring match counts a COMMENT that points here as a second
  // mount, and such comments are exactly what the design needs: the surfaces
  // that stopped managing the modal stack say where that work went.
  it('mounts once, at the app root', () => {
    const srcRoot = join(frontendRoot, 'src')
    const MOUNTS = /from\s+'[^']*\/ElevationPromptHost'|<ElevationPromptHost[\s/>]/
    const mounts = collectFiles(srcRoot, {
      matches: name => (name.endsWith('.ts') || name.endsWith('.tsx')) && !name.includes('.test.'),
      alsoSkip: new Set(['generated']),
    })
      .filter(file => MOUNTS.test(readFileSync(file, 'utf-8')))
      .map(file => posixRelative(frontendRoot, file))
      .sort()

    expect(
      mounts,
      'The step-up prompt mounts ONCE, in src/app.tsx. A second mount replaces '
      + 'the first registration; a mount inside a panel answers only the '
      + 'refusals raised in that panel.',
    ).toEqual(['src/app.tsx'])
  })
})
