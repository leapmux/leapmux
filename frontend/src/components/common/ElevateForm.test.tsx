import { fireEvent, render, screen } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { ElevateForm } from '~/components/common/ElevateForm'
import { resetSystemInfoMock, setSystemInfoMock } from '~/test-support/systemInfoMock'

const mockUser = vi.fn()
const mockElevateWithPassword = vi.fn()
const mockElevateWithPasskey = vi.fn()

// The CONTEXT runs the ceremony and adopts the deadline, so this test mocks
// the context at that seam. The form itself writes no elevation state.
vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    user: mockUser,
    elevateWithPassword: (...args: unknown[]) => mockElevateWithPassword(...args),
    elevateWithPasskey: (...args: unknown[]) => mockElevateWithPasskey(...args),
  }),
}))

vi.mock('~/lib/systemInfo', async () => {
  const m = await import('~/test-support/systemInfoMock')
  return m.systemInfoMock
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

  it('offers the passkey option only when the account has one that this hub can run', () => {
    mockUser.mockReturnValue({ ...passwordUser, passkeyCount: 1 })
    renderForm()
    expect(screen.getByTestId('elevate-passkey')).toBeInTheDocument()
  })

  it('hides the passkey option when the hub runs no ceremony at this address', () => {
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
 * remedies that go to three different people. The copy this replaced specified an
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

  it('specifies the insecure page, and no administrator', () => {
    setSystemInfoMock({ passkeyBlocker: 'insecure-context' })
    renderForm()

    const message = screen.getByTestId('elevate-impossible')
    expect(message).toHaveTextContent(/Open the hub over HTTPS/i)
    expect(message).not.toHaveTextContent(/administrator/i)
  })

  it('specifies the browser when the page is secure and WebAuthn is absent', () => {
    setSystemInfoMock({ passkeyBlocker: 'no-webauthn' })
    renderForm()

    const message = screen.getByTestId('elevate-impossible')
    expect(message).toHaveTextContent(/browser does not support passkeys/i)
    expect(message).not.toHaveTextContent(/administrator/i)
  })

  it('specifies the administrator when the hub does not serve this origin', () => {
    setSystemInfoMock({ passkeyBlocker: 'origin-not-allowed' })
    renderForm()

    expect(screen.getByTestId('elevate-impossible')).toHaveTextContent(/administrator/i)
  })

  // The blocker must not replace the OTHER dead-end copy. An account with no factor
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
 * The warning for a host whose OAuth round trip discards the page.
 *
 * The provider option is a full-document navigation out of the app. The
 * standalone `/elevate` route returns the browser to itself, so nothing is
 * lost; the in-app dialog can only return to `/`, so a half-filled form goes
 * with it. The FLAG is the host's and the WORDING is the form's, because only
 * the form knows whether a provider option is on screen at all.
 */
describe('elevateForm oauth round-trip warning', () => {
  const providerOnlyUser = {
    ...passwordUser,
    passwordSet: false,
    passkeyCount: 0,
    oauthProviders: [{ id: 'github-1', name: 'GitHub', enabled: true }],
    mayElevateThroughAProvider: true,
  }

  beforeEach(() => {
    vi.clearAllMocks()
    resetSystemInfoMock()
    mockUser.mockReturnValue(providerOnlyUser)
  })

  // The provider option leaves the app, and it USED to carry a note saying so
  // and warning that the user would lose what they had typed. The note is gone
  // because the loss is gone: the address survives the round trip through the
  // storage gateway, and the caller now passes the address the user is on, so
  // the browser returns to the panel it left. A note that describes neither
  // cost is one more thing to read on a screen that asks for a password.
  it('states no cost, because the round trip no longer has one', () => {
    render(() => <ElevateForm oauthRedirect="/?prefs=account" onElevated={vi.fn()} />)

    expect(screen.getByTestId('elevate-oauth-github-1')).toBeInTheDocument()
    expect(screen.queryByTestId('elevate-oauth-discards-typed-text')).toBeNull()
    expect(screen.queryByText(/home page/i)).toBeNull()
  })

  // The link carries the address the caller gave, so the hub returns the
  // browser to the panel it left rather than to the app root.
  it('carries the caller address into the provider link', () => {
    render(() => <ElevateForm oauthRedirect="/?prefs=account" onElevated={vi.fn()} />)

    expect(screen.getByTestId('elevate-oauth-github-1').getAttribute('href'))
      .toBe(`/auth/oauth/github-1/reauth?redirect=${encodeURIComponent('/?prefs=account')}`)
  })
})

/**
 * The account this password belongs to, carried in the form for the password
 * manager.
 *
 * A re-authentication form asks for a password and nothing else, so a manager
 * has no field to match a stored entry's user against. The remedy that every
 * manager and every sign-in-form guide specifies is the same: an
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
  // fields skips a field with no box, which removes the whole reason to carry
  // one.
  it('keeps the hint in the layout rather than removing it', () => {
    renderForm()
    const username = screen.getByTestId('elevate-username')
    expect(username).not.toHaveAttribute('hidden')
    expect(username.getAttribute('class')).toBeTruthy()
    expect((username as HTMLInputElement).style.display).not.toBe('none')
  })

  // It is not a field the user fills, so it must not take a tab stop, and
  // assistive technology must not announce it as one.
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
