import { Code, ConnectError } from '@connectrpc/connect'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { CaptchaProvider } from '~/generated/leapmux/v1/auth_pb'
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

// Partial mock: only the ceremony is faked. passkeyErrorMessage is the real
// classifier, so these tests exercise the same cancel-vs-failure rule the
// component ships with.
vi.mock('~/lib/webauthn', async importOriginal => ({
  ...await importOriginal<typeof import('~/lib/webauthn')>(),
  startRegistration: vi.fn().mockResolvedValue('{"id":"cred"}'),
}))

vi.mock('~/lib/systemInfo', async () => {
  const m = await import('~/test-support/systemInfoMock')
  return m.systemInfoMock
})

const [mockCaptchaPayload, setMockCaptchaPayload] = createSignal<string | null>(null)
vi.mock('~/components/common/CaptchaField', async () => {
  const { createEffect } = await import('solid-js')
  return {
    CaptchaField: (props: { action: string, onPayload: (p: string | null) => void, onUnavailable: () => void }) => {
      createEffect(() => props.onPayload(mockCaptchaPayload()))
      return <div data-testid="captcha-field" data-action={props.action} />
    },
  }
})
vi.mock('~/components/common/CaptchaHoneypot', () => ({
  CaptchaHoneypot: () => <input data-testid="captcha-honeypot" type="text" name="website" />,
}))

function usernameInput() {
  return screen.getByLabelText('Username') as HTMLInputElement
}

function displayNameInput() {
  return screen.getByLabelText('Display Name') as HTMLInputElement
}

function renderForm() {
  return render(() => (
    <SignupForm
      submitLabel="Create account"
      submittingLabel="Creating account..."
      onSuccess={() => {}}
    />
  ))
}

describe('signup form display-name mirror', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    resetSystemInfoMock()
    setMockCaptchaPayload(null)
    mockSignUp.mockResolvedValue({})
  })

  it('mirrors the username into the display name as it is typed', () => {
    renderForm()

    fireEvent.input(usernameInput(), { target: { value: 'alice' } })
    expect(displayNameInput().value).toBe('alice')

    // Keeps up with edits to the username, raw casing included: the slug is
    // lowercased at submit, but the display name keeps what the user typed.
    fireEvent.input(usernameInput(), { target: { value: 'Alice-dev' } })
    expect(displayNameInput().value).toBe('Alice-dev')
  })

  it('stops mirroring once the display name is edited directly', () => {
    renderForm()

    fireEvent.input(usernameInput(), { target: { value: 'alice' } })
    fireEvent.input(displayNameInput(), { target: { value: 'Alice Smith' } })
    expect(displayNameInput().value).toBe('Alice Smith')

    // Later username typing must not clobber the user's own name.
    fireEvent.input(usernameInput(), { target: { value: 'bob' } })
    expect(usernameInput().value).toBe('bob')
    expect(displayNameInput().value).toBe('Alice Smith')
  })

  it('does not re-arm the mirror after the display name is cleared', () => {
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
    setMockCaptchaPayload(null)
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

  it('hides password fields when passkey is selected', () => {
    renderForm()
    fireEvent.click(screen.getByRole('radio', { name: 'Passkey' }))
    expect(screen.queryByLabelText('Password')).not.toBeInTheDocument()
    expect(screen.queryByLabelText('Confirm Password')).not.toBeInTheDocument()
  })

  it('switches captcha action when the passkey method is selected', async () => {
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
