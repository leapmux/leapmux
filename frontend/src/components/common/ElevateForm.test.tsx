import { fireEvent, render, screen } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { ElevateForm } from '~/components/common/ElevateForm'
import { resetSystemInfoMock, setSystemInfoMock } from '~/test-support/systemInfoMock'

const mockUser = vi.fn()
const mockSetElevationExpiresAt = vi.fn()
const mockElevateWithPassword = vi.fn()
const mockElevateWithPasskey = vi.fn()

vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    user: mockUser,
    setElevationExpiresAt: mockSetElevationExpiresAt,
  }),
}))

vi.mock('~/lib/systemInfo', async () => {
  const m = await import('~/test-support/systemInfoMock')
  return m.systemInfoMock
})

vi.mock('~/lib/elevation', async () => {
  const actual = await vi.importActual<typeof import('~/lib/elevation')>('~/lib/elevation')
  return {
    ...actual,
    elevateWithPassword: (...args: unknown[]) => mockElevateWithPassword(...args),
    elevateWithPasskey: (...args: unknown[]) => mockElevateWithPasskey(...args),
  }
})

const passwordUser = {
  id: 'user-1',
  username: 'alice',
  displayName: 'Alice',
  passwordSet: true,
  passkeyCount: 0,
  oauthProviders: [],
  mayElevateThroughAProvider: false,
}

function renderForm(onElevated = vi.fn()) {
  render(() => <ElevateForm oauthRedirect="/" onElevated={onElevated} />)
  return onElevated
}

describe('elevateForm', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    resetSystemInfoMock()
    mockUser.mockReturnValue(passwordUser)
    mockElevateWithPassword.mockResolvedValue(undefined)
    mockElevateWithPasskey.mockResolvedValue(undefined)
  })

  it('proves a password and reports it', async () => {
    const onElevated = renderForm()
    fireEvent.input(screen.getByTestId('elevate-password'), { target: { value: 'secret' } })
    fireEvent.click(screen.getByTestId('elevate-password-submit'))

    await vi.waitFor(() => expect(mockElevateWithPassword).toHaveBeenCalledWith('secret'))
    expect(onElevated).toHaveBeenCalled()
  })

  it('reports the hub refusal verbatim', async () => {
    mockElevateWithPassword.mockRejectedValue(new Error('current password is incorrect'))
    const onElevated = renderForm()
    fireEvent.input(screen.getByTestId('elevate-password'), { target: { value: 'wrong' } })
    fireEvent.click(screen.getByTestId('elevate-password-submit'))

    expect(await screen.findByRole('alert')).toHaveTextContent('current password is incorrect')
    expect(onElevated).not.toHaveBeenCalled()
  })

  it('offers the passkey arm only when the account has one this hub can run', () => {
    mockUser.mockReturnValue({ ...passwordUser, passkeyCount: 1 })
    renderForm()
    expect(screen.getByTestId('elevate-passkey')).toBeInTheDocument()
  })

  it('hides the passkey arm when the hub runs no ceremony at this address', () => {
    // The per-origin answer: the hub HAS passkeys and this page's origin is
    // not one it serves, so every Begin would answer FailedPrecondition.
    setSystemInfoMock({ passkeyBlocker: 'origin-not-allowed' })
    mockUser.mockReturnValue({ ...passwordUser, passkeyCount: 1 })
    renderForm()
    expect(screen.queryByTestId('elevate-passkey')).not.toBeInTheDocument()
  })
})

/**
 * The dead end this form must describe correctly.
 *
 * An account whose only factor is a passkey can verify nothing here when the
 * page cannot run a ceremony, and the three blockers have three different
 * remedies that go to three different people. The copy this replaced named an
 * administrator for all of them -- wrong advice for both blockers the BROWSER
 * raises, because no address anybody publishes makes a plain-HTTP page
 * secure.
 */
describe('elevateForm dead-end copy', () => {
  const passkeyOnlyUser = {
    ...passwordUser,
    passwordSet: false,
    passkeyCount: 1,
  }

  beforeEach(() => {
    vi.clearAllMocks()
    resetSystemInfoMock()
    mockUser.mockReturnValue(passkeyOnlyUser)
  })

  it('names the insecure page, and no administrator', () => {
    setSystemInfoMock({ passkeyBlocker: 'insecure-context' })
    renderForm()

    const message = screen.getByTestId('elevate-impossible')
    expect(message).toHaveTextContent(/Open the hub over HTTPS/i)
    expect(message).not.toHaveTextContent(/administrator/i)
  })

  it('names the browser when the page is secure and WebAuthn is absent', () => {
    setSystemInfoMock({ passkeyBlocker: 'no-webauthn' })
    renderForm()

    const message = screen.getByTestId('elevate-impossible')
    expect(message).toHaveTextContent(/browser does not support passkeys/i)
    expect(message).not.toHaveTextContent(/administrator/i)
  })

  it('names the administrator when the hub does not serve this origin', () => {
    setSystemInfoMock({ passkeyBlocker: 'origin-not-allowed' })
    renderForm()

    expect(screen.getByTestId('elevate-impossible')).toHaveTextContent(/administrator/i)
  })

  // The blocker must not steal the OTHER dead end. An account with no factor
  // at all can still set a first password after a fresh sign-in, so it keeps
  // the copy that says so -- whatever the page can or cannot run.
  it('keeps the no-factor copy for an account that holds no passkey', () => {
    setSystemInfoMock({ passkeyBlocker: 'insecure-context' })
    mockUser.mockReturnValue({ ...passwordUser, passwordSet: false, passkeyCount: 0 })
    renderForm()

    expect(screen.getByTestId('elevate-impossible'))
      .toHaveTextContent(/nothing to verify with yet/i)
  })
})

/**
 * The account this password belongs to, carried in the form for the password
 * manager.
 *
 * A re-authentication form asks for a password and nothing else, so a manager
 * has no field to match a stored entry's user against. The remedy every
 * manager and every sign-in-form guide names is the same: an
 * `autocomplete="username"` field beside the password.
 */
describe('elevateForm password-manager hints', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    resetSystemInfoMock()
    mockUser.mockReturnValue(passwordUser)
  })

  it('carries the signed-in username as an autocomplete hint', () => {
    renderForm()
    const username = screen.getByTestId('elevate-username')
    expect(username).toHaveValue('alice')
    expect(username).toHaveAttribute('autocomplete', 'username')
  })

  // NOT readonly. A manager that fills a login form writes the username as
  // well as the password, and several refuse a form whose username field they
  // cannot write -- which loses the fill this field exists to enable. Nothing
  // reads the field back: the submit takes the password signal, not the DOM.
  it('lets a manager write the hint', () => {
    renderForm()
    expect(screen.getByTestId('elevate-username')).not.toHaveAttribute('readonly')
  })

  // NOT `display: none` and NOT `hidden`: a manager that walks the rendered
  // fields skips a field with no box, which defeats the whole point of
  // carrying one.
  it('keeps the hint in the layout rather than removing it', () => {
    renderForm()
    const username = screen.getByTestId('elevate-username')
    expect(username).not.toHaveAttribute('hidden')
    expect(username.getAttribute('class')).toBeTruthy()
    expect((username as HTMLInputElement).style.display).not.toBe('none')
  })

  // It is not a field the user fills, so it must not take a tab stop or be
  // announced as one.
  it('leaves the hint out of the tab order and the accessibility tree', () => {
    renderForm()
    const username = screen.getByTestId('elevate-username')
    expect(username).toHaveAttribute('tabindex', '-1')
    expect(username).toHaveAttribute('aria-hidden', 'true')
  })

  it('tells the manager the password field is the current one', () => {
    renderForm()
    expect(screen.getByTestId('elevate-password')).toHaveAttribute('autocomplete', 'current-password')
  })

  // An account with no password renders no password form at all, so it must
  // not leave a stray username field behind either.
  it('carries no hint when there is no password to fill', () => {
    mockUser.mockReturnValue({ ...passwordUser, passwordSet: false, passkeyCount: 1 })
    renderForm()
    expect(screen.queryByTestId('elevate-username')).not.toBeInTheDocument()
  })
})
