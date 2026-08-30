import { Code, ConnectError } from '@connectrpc/connect'
import { createMemoryHistory, MemoryRouter, Route } from '@solidjs/router'
/// <reference types="vitest/globals" />
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { CaptchaProvider } from '~/generated/proto/leapmux/v1/auth_pb'
import { mockLoadSystemInfo, resetSystemInfoMock, setSystemInfoMock } from '~/test-support/systemInfoMock'

import { RecoverCompletePage } from './RecoverCompletePage'

const mockCompleteAccountRecovery = vi.fn()

vi.mock('~/api/clients', () => ({
  authClient: {
    completeAccountRecovery: (...args: unknown[]) => mockCompleteAccountRecovery(...args),
  },
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
  CaptchaHoneypot: (props: { value: string, onInput: (v: string) => void }) => (
    <input
      data-testid="captcha-honeypot"
      type="text"
      name="website"
      value={props.value}
      onInput={e => props.onInput(e.currentTarget.value)}
    />
  ),
}))

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
    mockCompleteAccountRecovery.mockResolvedValue({})
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
    mockCompleteAccountRecovery.mockReturnValue(new Promise((resolve) => {
      resolveReset = resolve
    }))
    renderRecoverCompletePage('/recover-account/complete?token=recovery-token')
    fireEvent.input(await screen.findByLabelText('New Password'), { target: { value: 'newpass123' } })
    fireEvent.input(screen.getByLabelText('Confirm Password'), { target: { value: 'newpass123' } })
    fireEvent.click(screen.getByRole('button', { name: 'Set new password' }))
    expect(await screen.findByRole('button', { name: 'Setting…' })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Setting…' }))
    expect(mockCompleteAccountRecovery).toHaveBeenCalledOnce()
    resolveReset({})
    expect(await screen.findByTestId('login-page')).toBeInTheDocument()
  })

  it('passes the complete_account_recovery action to the captcha field', async () => {
    setSystemInfoMock({ captchaEnabled: true, captchaProvider: CaptchaProvider.TURNSTILE, captchaSiteKey: '1x00000000000000000000AA' })
    renderRecoverCompletePage('/recover-account/complete?token=recovery-token')
    await vi.waitFor(() => {
      expect(screen.getByTestId('captcha-field')).toHaveAttribute('data-action', 'complete_account_recovery')
    })
  })

  it('re-enables Set new password after a failed attempt and refreshes captcha info on PermissionDenied', async () => {
    mockCompleteAccountRecovery.mockRejectedValue(new ConnectError('denied', Code.PermissionDenied))
    renderRecoverCompletePage('/recover-account/complete?token=recovery-token')
    fireEvent.input(await screen.findByLabelText('New Password'), { target: { value: 'newpass123' } })
    fireEvent.input(screen.getByLabelText('Confirm Password'), { target: { value: 'newpass123' } })
    fireEvent.click(screen.getByRole('button', { name: 'Set new password' }))
    await vi.waitFor(() => {
      expect(mockCompleteAccountRecovery).toHaveBeenCalledOnce()
    })
    expect(await screen.findByRole('button', { name: 'Set new password' })).toBeEnabled()
    expect(mockLoadSystemInfo).toHaveBeenCalledWith(true)
  })
})
