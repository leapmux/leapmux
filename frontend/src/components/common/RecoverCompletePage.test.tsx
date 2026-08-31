import { Code, ConnectError } from '@connectrpc/connect'
import { createMemoryHistory, MemoryRouter, Route } from '@solidjs/router'
/// <reference types="vitest/globals" />
import { fireEvent, render, screen } from '@solidjs/testing-library'

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { CaptchaProvider } from '~/generated/proto/leapmux/v1/auth_pb'
import { setMockCaptchaPayload } from '~/test-support/captchaMocks'
import { mockLoadSystemInfo, resetSystemInfoMock, setSystemInfoMock } from '~/test-support/systemInfoMock'

import { RecoverCompletePage } from './RecoverCompletePage'

const mockCompleteAccountRecoveryPassword = vi.fn()
const mockBeginAccountRecoveryPasskey = vi.fn()
const mockFinishAccountRecoveryPasskey = vi.fn()

vi.mock('~/api/clients', () => ({
  authClient: {
    completeAccountRecoveryPassword: (...args: unknown[]) => mockCompleteAccountRecoveryPassword(...args),
    beginAccountRecoveryPasskey: (...args: unknown[]) => mockBeginAccountRecoveryPasskey(...args),
    finishAccountRecoveryPasskey: (...args: unknown[]) => mockFinishAccountRecoveryPasskey(...args),
  },
}))

vi.mock('~/lib/webauthn', async importOriginal => ({
  ...await importOriginal<typeof import('~/lib/webauthn')>(),
  startRegistration: vi.fn().mockResolvedValue('{"id":"cred"}'),
}))

vi.mock('~/lib/systemInfo', async () => {
  const m = await import('~/test-support/systemInfoMock')
  return m.systemInfoMock
})

vi.mock('~/components/common/CaptchaField', async () => (await import('~/test-support/captchaMocks')).captchaFieldMock)
vi.mock('~/components/common/CaptchaHoneypot', async () => (await import('~/test-support/captchaMocks')).captchaHoneypotMock)

function renderRecoverCompletePage(initialPath: string) {
  const history = createMemoryHistory()
  history.set({ value: initialPath, replace: true, scroll: false })
  return render(() => (
    <MemoryRouter history={history}>
      <Route path="/recover-account/complete" component={RecoverCompletePage} />
      <Route path="/login" component={() => <div data-testid="login-page" />} />
      <Route path="/recover-account" component={() => <div data-testid="recover-page" />} />
    </MemoryRouter>
  ))
}

describe('recover complete page', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    resetSystemInfoMock()
    setMockCaptchaPayload(null)
    mockCompleteAccountRecoveryPassword.mockResolvedValue({})
    mockBeginAccountRecoveryPasskey.mockResolvedValue({
      sessionId: 'session-1',
      optionsJson: '{}',
      rpId: 'localhost',
    })
    mockFinishAccountRecoveryPasskey.mockResolvedValue({})
  })

  it('shows a missing-token error when the recovery link has no token', async () => {
    renderRecoverCompletePage('/recover-account/complete')
    expect(await screen.findByText('Missing recovery token. Open the link from your email.')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Request a new link' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Set new password' })).not.toBeInTheDocument()
  })

  it('keeps Set new password disabled until the new password fields match', async () => {
    renderRecoverCompletePage('/recover-account/complete?token=recovery-token')
    expect(await screen.findByRole('button', { name: 'Set new password' })).toBeDisabled()
    fireEvent.input(screen.getByLabelText('New Password'), { target: { value: 'newpass123' } })
    expect(screen.getByRole('button', { name: 'Set new password' })).toBeDisabled()
    fireEvent.input(screen.getByLabelText('Confirm Password'), { target: { value: 'newpass123' } })
    expect(screen.getByRole('button', { name: 'Set new password' })).toBeEnabled()
  })

  it('navigates to login after a successful recovery and does not re-enable submit', async () => {
    let resolveReset!: (value: unknown) => void
    mockCompleteAccountRecoveryPassword.mockReturnValue(new Promise((resolve) => {
      resolveReset = resolve
    }))
    renderRecoverCompletePage('/recover-account/complete?token=recovery-token')
    fireEvent.input(await screen.findByLabelText('New Password'), { target: { value: 'newpass123' } })
    fireEvent.input(screen.getByLabelText('Confirm Password'), { target: { value: 'newpass123' } })
    fireEvent.click(screen.getByRole('button', { name: 'Set new password' }))
    expect(await screen.findByRole('button', { name: 'Recovering…' })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Recovering…' }))
    expect(mockCompleteAccountRecoveryPassword).toHaveBeenCalledOnce()
    expect(mockCompleteAccountRecoveryPassword).toHaveBeenCalledWith({
      token: 'recovery-token',
      newPassword: 'newpass123',
      captchaPayload: '',
      honeypot: '',
    })
    resolveReset({})
    expect(await screen.findByTestId('login-page')).toBeInTheDocument()
  })

  it('recovers with a passkey when the passkey method is selected', async () => {
    renderRecoverCompletePage('/recover-account/complete?token=recovery-token')
    await screen.findByRole('radiogroup', { name: 'Recovery method' })
    fireEvent.click(screen.getByRole('radio', { name: 'Passkey' }))
    expect(screen.queryByLabelText('New Password')).not.toBeInTheDocument()
    const submit = screen.getByRole('button', { name: 'Recover with passkey' })
    expect(submit).toBeEnabled()
    fireEvent.click(submit)
    await vi.waitFor(() => {
      expect(mockFinishAccountRecoveryPasskey).toHaveBeenCalledOnce()
    })
    expect(mockBeginAccountRecoveryPasskey).toHaveBeenCalledWith({
      token: 'recovery-token',
      captchaPayload: '',
      honeypot: '',
    })
    expect(mockCompleteAccountRecoveryPassword).not.toHaveBeenCalled()
    expect(await screen.findByTestId('login-page')).toBeInTheDocument()
  })

  it('switches the captcha action when the user selects the passkey method', async () => {
    setSystemInfoMock({ captchaEnabled: true, captchaProvider: CaptchaProvider.TURNSTILE, captchaSiteKey: '1x00000000000000000000AA' })
    renderRecoverCompletePage('/recover-account/complete?token=recovery-token')
    await vi.waitFor(() => {
      expect(screen.getByTestId('captcha-field')).toHaveAttribute('data-action', 'account_recovery_password')
    })
    fireEvent.click(screen.getByRole('radio', { name: 'Passkey' }))
    await vi.waitFor(() => {
      expect(screen.getByTestId('captcha-field')).toHaveAttribute('data-action', 'account_recovery_passkey')
    })
  })

  it('re-enables the submit after a failed passkey recovery', async () => {
    mockBeginAccountRecoveryPasskey.mockRejectedValue(new ConnectError('not found', Code.NotFound))
    renderRecoverCompletePage('/recover-account/complete?token=recovery-token')
    await screen.findByRole('radiogroup', { name: 'Recovery method' })
    fireEvent.click(screen.getByRole('radio', { name: 'Passkey' }))
    fireEvent.click(screen.getByRole('button', { name: 'Recover with passkey' }))
    await vi.waitFor(() => {
      expect(mockBeginAccountRecoveryPasskey).toHaveBeenCalledOnce()
    })
    expect(screen.getByRole('button', { name: 'Recover with passkey' })).toBeEnabled()
    expect(screen.queryByTestId('login-page')).not.toBeInTheDocument()
  })

  it('passes the account_recovery_password action to the captcha field', async () => {
    setSystemInfoMock({ captchaEnabled: true, captchaProvider: CaptchaProvider.TURNSTILE, captchaSiteKey: '1x00000000000000000000AA' })
    renderRecoverCompletePage('/recover-account/complete?token=recovery-token')
    await vi.waitFor(() => {
      expect(screen.getByTestId('captcha-field')).toHaveAttribute('data-action', 'account_recovery_password')
    })
  })

  it('re-enables Set new password after a failed attempt and refreshes captcha info on PermissionDenied', async () => {
    mockCompleteAccountRecoveryPassword.mockRejectedValue(new ConnectError('denied', Code.PermissionDenied))
    renderRecoverCompletePage('/recover-account/complete?token=recovery-token')
    fireEvent.input(await screen.findByLabelText('New Password'), { target: { value: 'newpass123' } })
    fireEvent.input(screen.getByLabelText('Confirm Password'), { target: { value: 'newpass123' } })
    fireEvent.click(screen.getByRole('button', { name: 'Set new password' }))
    await vi.waitFor(() => {
      expect(mockCompleteAccountRecoveryPassword).toHaveBeenCalledOnce()
    })
    expect(await screen.findByRole('button', { name: 'Set new password' })).toBeEnabled()
    expect(mockLoadSystemInfo).toHaveBeenCalledWith(true)
  })
})
