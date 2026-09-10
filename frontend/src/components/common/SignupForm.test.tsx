import { Code, ConnectError } from '@connectrpc/connect'
import { fireEvent, render, screen } from '@solidjs/testing-library'

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { CaptchaProvider } from '~/generated/proto/leapmux/v1/auth_pb'
import { resetCaptchaMocks } from '~/test-support/captchaMocks'
import { mockLoadSystemInfo, resetSystemInfoMock, setSystemInfoMock } from '~/test-support/systemInfoMock'

import { SignupForm } from './SignupForm'

const mockSignUp = vi.fn<(...args: unknown[]) => Promise<unknown>>()
const mockBeginPasskeySignUp = vi.fn<(...args: unknown[]) => Promise<unknown>>()
const mockFinishPasskeySignUp = vi.fn<(...args: unknown[]) => Promise<unknown>>()

vi.mock('~/api/clients', () => ({
  authClient: {
    signUp: (...args: unknown[]) => mockSignUp(...args),
    beginPasskeySignUp: (...args: unknown[]) => mockBeginPasskeySignUp(...args),
    finishPasskeySignUp: (...args: unknown[]) => mockFinishPasskeySignUp(...args),
    login: vi.fn(),
    logout: vi.fn(),
    getCurrentUser: vi.fn(),
  },
}))

// Mock only the passkey ceremony. Keep the real passkeyErrorMessage classifier to distinguish cancellation from failure.
vi.mock('~/lib/webauthn', async importOriginal => ({
  ...await importOriginal<typeof import('~/lib/webauthn')>(),
  startRegistration: vi.fn().mockResolvedValue('{"id":"cred"}'),
}))

vi.mock('~/lib/systemInfo', async () => {
  const m = await import('~/test-support/systemInfoMock')
  return m.systemInfoMock
})

vi.mock('~/components/common/CaptchaField', async () => {
  const m = await import('~/test-support/captchaMocks')
  return m.captchaFieldMock
})
vi.mock('~/components/common/CaptchaHoneypot', async () => {
  const m = await import('~/test-support/captchaMocks')
  return m.captchaHoneypotMock
})

function usernameInput() {
  return screen.getByLabelText('Username') as HTMLInputElement
}

function displayNameInput() {
  return screen.getByLabelText('Display Name') as HTMLInputElement
}

function renderForm(overrides: Partial<Parameters<typeof SignupForm>[0]> = {}) {
  return render(() => (
    <SignupForm
      submitLabel="Create account"
      submittingLabel="Creating account..."
      onSuccess={() => {}}
      {...overrides}
    />
  ))
}

/**
 * Read the disabled control description through aria-describedby, as a screen reader does.
 * Tooltip keeps this description while the control stays disabled.
 * A native title would instead make a long explanation the accessible name.
 */
function reasonOf(el: Element): string {
  const describedBy = el.getAttribute('aria-describedby')
  expect(describedBy).toBeTruthy()
  return document.getElementById(describedBy!)?.textContent ?? ''
}

describe('signup form display-name mirror', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    resetSystemInfoMock()
    resetCaptchaMocks()
    mockSignUp.mockResolvedValue({})
  })

  it('mirrors the username into the display name as the user types', () => {
    renderForm()

    fireEvent.input(usernameInput(), { target: { value: 'alice' } })
    expect(displayNameInput().value).toBe('alice')

    // Follows edits to the username, raw casing included: the slug is
    // lowercased at submit, but the display name keeps what the user typed.
    fireEvent.input(usernameInput(), { target: { value: 'Alice-dev' } })
    expect(displayNameInput().value).toBe('Alice-dev')
  })

  it('stops mirroring once the user edits the display name directly', () => {
    renderForm()

    fireEvent.input(usernameInput(), { target: { value: 'alice' } })
    fireEvent.input(displayNameInput(), { target: { value: 'Alice Smith' } })
    expect(displayNameInput().value).toBe('Alice Smith')

    // Later username typing must not overwrite the user's own name.
    fireEvent.input(usernameInput(), { target: { value: 'bob' } })
    expect(usernameInput().value).toBe('bob')
    expect(displayNameInput().value).toBe('Alice Smith')
  })

  it('does not re-arm the mirror after the user clears the display name', () => {
    renderForm()

    fireEvent.input(usernameInput(), { target: { value: 'alice' } })
    fireEvent.input(displayNameInput(), { target: { value: 'x' } })
    fireEvent.input(displayNameInput(), { target: { value: '' } })

    fireEvent.input(usernameInput(), { target: { value: 'bobby' } })
    expect(displayNameInput().value).toBe('')
  })
})

describe('signup form passkey path', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    resetSystemInfoMock()
    resetCaptchaMocks()
  })

  it('requires email before submit', () => {
    // The hub requires an email only when SMTP (the verification channel)
    // is configured.
    setSystemInfoMock({ emailEnabled: true })
    renderForm()
    fireEvent.input(usernameInput(), { target: { value: 'alice' } })
    fireEvent.click(screen.getByRole('radio', { name: 'Passkey' }))
    expect(screen.getByRole('button', { name: 'Sign up with passkey' })).toBeDisabled()
    expect(mockBeginPasskeySignUp).not.toHaveBeenCalled()
  })

  // A form that cannot run a passkey ceremony must retain password signup.
  // When the hub refuses passkeys, remove the passkey option because the deployment cannot serve it.
  it('drops the passkey option when the hub does not serve this origin', () => {
    setSystemInfoMock({ passkeyBlocker: 'origin-not-allowed' })
    renderForm()
    expect(screen.queryByRole('radio', { name: 'Passkey' })).not.toBeInTheDocument()
    expect(screen.getByRole('radio', { name: 'Password' })).toBeInTheDocument()
    expect(screen.getByLabelText('New Password')).toBeInTheDocument()
  })

  // When the browser refuses passkeys, retain the option and explain why it is disabled.
  // The user can change browsers or origins.
  it.each([
    ['the page is not secure', 'insecure-context' as const, /secure page/i],
    ['the browser has no WebAuthn', 'no-webauthn' as const, /does not support passkeys/i],
  ])('keeps the passkey option and says why when %s', (_case, blocker, expected) => {
    setSystemInfoMock({ passkeyBlocker: blocker })
    renderForm()

    const passkey = screen.getByRole('radio', { name: 'Passkey' })
    expect(passkey).toHaveAttribute('aria-disabled', 'true')
    expect(passkey).not.toBeDisabled()
    expect(passkey).not.toHaveAttribute('title')
    expect(reasonOf(passkey)).toMatch(expected)
    expect(screen.getByLabelText('New Password')).toBeInTheDocument()
  })

  it('hides the password fields when the user selects passkey', () => {
    renderForm()
    // Check that the password fields exist before switching methods.
    // Use the actual New Password label, or an absence check could pass when the form never rendered it.
    expect(screen.getByLabelText('New Password')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('radio', { name: 'Passkey' }))
    expect(screen.queryByLabelText('New Password')).not.toBeInTheDocument()
    expect(screen.queryByLabelText('Confirm Password')).not.toBeInTheDocument()
  })

  it('switches the captcha action when the user selects the passkey method', async () => {
    setSystemInfoMock({ captchaEnabled: true, captchaProvider: CaptchaProvider.TURNSTILE, captchaSiteKey: '1x00000000000000000000AA' })
    renderForm()
    await vi.waitFor(() => {
      expect(screen.getByTestId('captcha-field')).toHaveAttribute('data-action', 'signup')
    })
    fireEvent.click(screen.getByRole('radio', { name: 'Passkey' }))
    await vi.waitFor(() => {
      expect(screen.getByTestId('captcha-field')).toHaveAttribute('data-action', 'passkey_signup')
    })
  })

  it('re-enables Create account after a failed attempt and refreshes captcha info on PermissionDenied', async () => {
    mockSignUp.mockRejectedValue(new ConnectError('denied', Code.PermissionDenied))
    renderForm()
    fireEvent.input(usernameInput(), { target: { value: 'alice' } })
    fireEvent.input(screen.getByLabelText('Email'), { target: { value: 'alice@example.com' } })
    fireEvent.input(screen.getByLabelText('New Password'), { target: { value: 'newpass123' } })
    fireEvent.input(screen.getByLabelText('Confirm Password'), { target: { value: 'newpass123' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create account' }))
    await vi.waitFor(() => {
      expect(mockSignUp).toHaveBeenCalledOnce()
    })
    expect(await screen.findByRole('button', { name: 'Create account' })).toBeEnabled()
    expect(mockLoadSystemInfo).toHaveBeenCalledWith(true)
  })
})

describe('signup username validation', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    resetSystemInfoMock()
    resetCaptchaMocks()
    mockSignUp.mockResolvedValue({ user: { id: 'created-user' } })
  })

  function fillCredentials(username: string) {
    fireEvent.input(usernameInput(), { target: { value: username } })
    fireEvent.input(screen.getByLabelText('New Password'), { target: { value: 'newpass123' } })
    fireEvent.input(screen.getByLabelText('Confirm Password'), { target: { value: 'newpass123' } })
  }

  it.each(['admin', 'Admin', ' ADMIN '])('rejects reserved usernames before sending a request: %s', async (username) => {
    const onSuccess = vi.fn()
    renderForm({ onSuccess })
    fillCredentials(username)
    fireEvent.click(screen.getByRole('button', { name: 'Create account' }))
    expect(await screen.findByText(/reserved username/i)).toBeVisible()
    expect(mockSignUp).not.toHaveBeenCalled()
    expect(onSuccess).not.toHaveBeenCalled()
  })

  it('permits the administrator name for the first-account setup form', async () => {
    const onSuccess = vi.fn()
    renderForm({ allowAdminUsername: true, onSuccess })
    fillCredentials('admin')
    fireEvent.click(screen.getByRole('button', { name: 'Create account' }))
    await vi.waitFor(() => expect(onSuccess).toHaveBeenCalledOnce())
    expect(mockSignUp).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ username: 'admin', password: 'newpass123' }))
    expect(screen.queryByText(/reserved username/i)).not.toBeInTheDocument()
  })
})
