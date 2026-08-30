import { Code, ConnectError } from '@connectrpc/connect'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { CaptchaProvider } from '~/generated/proto/leapmux/v1/auth_pb'
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

// Partial mock: this file fakes only the ceremony. passkeyErrorMessage is the real
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

/**
 * The reason a disabled control carries, read the way a screen reader gets it.
 *
 * <Tooltip> leaves an offscreen description in `aria-describedby` for as long
 * as the control is disabled. It is NOT `title`: a reason long enough to be
 * worth reading becomes the control's accessible name on `title`.
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
    setMockCaptchaPayload(null)
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

  // The other branch of the condition that this form tests, which nothing
  // exercised. A page that cannot run a ceremony must not offer to sign
  // somebody up with one, and the password option has to survive -- it is the
  // only remaining way to sign up.
  //
  // The HUB's refusal is a property of the deployment, so the option goes.
  it('drops the passkey option when the hub does not serve this origin', () => {
    setSystemInfoMock({ passkeyBlocker: 'origin-not-allowed' })
    renderForm()
    expect(screen.queryByRole('radio', { name: 'Passkey' })).not.toBeInTheDocument()
    expect(screen.getByRole('radio', { name: 'Password' })).toBeInTheDocument()
    expect(screen.getByLabelText('New Password')).toBeInTheDocument()
  })

  // The BROWSER's refusal is something the reader can clear by moving, so the
  // option stays and carries the reason.
  it.each([
    ['the page is not secure', 'insecure-context' as const, /secure page/i],
    ['the browser has no WebAuthn', 'no-webauthn' as const, /does not support passkeys/i],
  ])('keeps the passkey option and says why when %s', (_case, blocker, expected) => {
    setSystemInfoMock({ passkeyBlocker: blocker })
    renderForm()

    const passkey = screen.getByRole('radio', { name: 'Passkey' })
    expect(passkey).toBeDisabled()
    expect(passkey).not.toHaveAttribute('title')
    expect(reasonOf(passkey)).toMatch(expected)
    expect(screen.getByLabelText('New Password')).toBeInTheDocument()
  })

  it('hides the password fields when the user selects passkey', () => {
    renderForm()
    // Present FIRST, then gone. The label reads "New Password", so the
    // absence assertion this replaced queried a label that never existed --
    // it passed whether the fields rendered or not.
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
