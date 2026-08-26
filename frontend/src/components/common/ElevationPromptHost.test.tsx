import { readFileSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { Code, ConnectError } from '@connectrpc/connect'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { elevationInterceptor } from '~/api/transport'
import { ElevationPromptHost } from '~/components/common/ElevationPromptHost'
import { elevationPrompting, promptForElevation, setElevationPrompter } from '~/lib/elevationPrompt'
import { collectFiles } from '~/test-support/sourceTree'

const mockUser = vi.fn<() => Record<string, unknown> | null>(() => ({
  id: 'user-1',
  username: 'alice',
  passwordSet: true,
  passkeyCount: 0,
  oauthProviders: [],
}))
const mockSetElevationExpiresAt = vi.fn()
const mockElevateWithPassword = vi.fn()

vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    user: mockUser,
    setElevationExpiresAt: mockSetElevationExpiresAt,
  }),
}))

vi.mock('~/lib/elevation', async () => {
  const actual = await vi.importActual<typeof import('~/lib/elevation')>('~/lib/elevation')
  return { ...actual, elevateWithPassword: (...a: unknown[]) => mockElevateWithPassword(...a) }
})

vi.mock('~/lib/systemInfo', async () => {
  const actual = await vi.importActual<typeof import('~/lib/systemInfo')>('~/lib/systemInfo')
  return { ...actual, passkeyBlocker: () => 'origin-not-allowed' as const, passkeysUsableHere: () => false }
})

describe('elevationPromptHost', () => {
  beforeEach(() => {
    vi.clearAllMocks()
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
    render(() => <ElevationPromptHost />)
    expect(screen.queryByRole('dialog', { name: 'Verify your identity' })).not.toBeInTheDocument()
  })

  it('opens the prompt and resolves true once a factor is proven', async () => {
    render(() => <ElevationPromptHost />)

    const answered = promptForElevation()
    expect(await screen.findByRole('dialog', { name: 'Verify your identity' })).toBeInTheDocument()
    expect(elevationPrompting()).toBe(true)

    fireEvent.input(screen.getByTestId('elevate-password'), { target: { value: 'secret' } })
    fireEvent.click(screen.getByTestId('elevate-password-submit'))

    await expect(answered).resolves.toBe(true)
    expect(mockElevateWithPassword).toHaveBeenCalledWith('secret')
    expect(elevationPrompting()).toBe(false)
  })

  // Dismissing REPORTS the cancellation. Silence would leave the page looking
  // exactly as it did before the click, with the new password still typed in
  // and nothing saying it was not saved.
  it('resolves false when the prompt is dismissed', async () => {
    render(() => <ElevationPromptHost />)

    const answered = promptForElevation()
    await screen.findByRole('dialog', { name: 'Verify your identity' })
    fireEvent.click(screen.getByRole('button', { name: /close/i }))

    await expect(answered).resolves.toBe(false)
    expect(elevationPrompting()).toBe(false)
  })

  // Two requests refused at the same instant must not stack two dialogs. The
  // gate this replaced had to build a provider to prevent exactly that, and
  // proving a factor in the top one dropped the other's action.
  it('shares one prompt across concurrent refusals', async () => {
    render(() => <ElevationPromptHost />)

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
    render(() => <ElevationPromptHost />)

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
  // The request must not be retried: the user declined to prove a factor, so a
  // second attempt is refused identically.
  it('rethrows the hub refusal when the dialog is dismissed', async () => {
    render(() => <ElevationPromptHost />)

    const next = vi.fn(async () => {
      throw elevationRequired()
    })
    const answered = elevationInterceptor(next)(unaryReq())

    await screen.findByRole('dialog', { name: 'Verify your identity' })
    fireEvent.click(screen.getByRole('button', { name: /close/i }))

    await expect(answered).rejects.toThrow('recent sign-in')
    expect(next).toHaveBeenCalledTimes(1)
  })

  // Unmounted, a refusal must rethrow the hub's message rather than await a
  // promise nothing can settle.
  it('unregisters on unmount', async () => {
    const { unmount } = render(() => <ElevationPromptHost />)
    unmount()
    await expect(promptForElevation()).resolves.toBe(false)
  })

  // Where it mounts is the whole behaviour, so it is pinned here rather than
  // left to a reviewer. The transport refuses on an RPC from ANY surface, so a
  // host mounted inside one panel answers only that panel's refusals, and every
  // other surface shows the hub's raw refusal text with no prompt and no way
  // forward. That is why this component left `components/settings`. It is a
  // latent failure rather than a live one -- the Account panel issues every
  // elevation-gated call today -- and a latent failure is exactly what a guard
  // is for: the call that exposes it would be added somewhere else entirely,
  // by someone with no reason to look here.
  //
  // A source scan rather than a render: mounting is a fact about the app tree,
  // and rendering the real root here would need the whole provider stack, the
  // router and the platform bridge.
  it('mounts once, at the app root', () => {
    const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..', '..')
    const srcRoot = join(frontendRoot, 'src')
    const importers = collectFiles(srcRoot, {
      matches: name => (name.endsWith('.ts') || name.endsWith('.tsx')) && !name.includes('.test.'),
      alsoSkip: new Set(['generated']),
    })
      .filter(file => readFileSync(file, 'utf-8').includes('ElevationPromptHost'))
      .map(file => relative(frontendRoot, file))
      .sort()

    expect(
      importers,
      'The step-up prompt mounts ONCE, in src/app.tsx. A second mount replaces '
      + 'the first registration; a mount inside a panel answers only the '
      + 'refusals raised in that panel.',
    ).toEqual(['src/app.tsx', 'src/components/common/ElevationPromptHost.tsx'])
  })
})
