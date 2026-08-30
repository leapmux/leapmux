import type { CaptchaFieldProps } from './CaptchaField'

/// <reference types="vitest/globals" />
import { render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { CaptchaProvider } from '~/generated/proto/leapmux/v1/auth_pb'
import { CaptchaField } from './CaptchaField'

// The provider getter is backed by a real signal so the dispatcher's
// reactivity (a runtime provider switch reaching the form without a
// reload) is exercised, not assumed.
const [provider, setProvider] = createSignal<CaptchaProvider>(CaptchaProvider.ALTCHA)
vi.mock('~/lib/systemInfo', () => ({
  getCaptchaProvider: () => provider(),
}))

const altchaMounted = vi.fn((props: Record<string, unknown>) => <div data-testid="altcha" data-props={JSON.stringify(props)} />)
const turnstileMounted = vi.fn((props: Record<string, unknown>) => <div data-testid="turnstile" data-props={JSON.stringify(props)} />)
const recaptchaMounted = vi.fn((props: Record<string, unknown>) => <div data-testid="recaptcha" data-props={JSON.stringify(props)} />)
// vi.mock factories are hoisted above the const declarations; the
// wrapper closures defer the access until the mocked module is loaded.
vi.mock('./AltchaField', () => ({ AltchaField: (props: Record<string, unknown>) => altchaMounted(props) }))
vi.mock('./TurnstileField', () => ({ TurnstileField: (props: Record<string, unknown>) => turnstileMounted(props) }))
vi.mock('./RecaptchaV3Field', () => ({ RecaptchaV3Field: (props: Record<string, unknown>) => recaptchaMounted(props) }))

function renderDispatcher() {
  return render(() => (
    <CaptchaField action="login" onPayload={vi.fn()} onUnavailable={vi.fn()} ref={vi.fn()} />
  ))
}

describe('captchaField dispatcher', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    setProvider(CaptchaProvider.ALTCHA)
  })

  it('mounts the altcha field by default and forwards the action', () => {
    renderDispatcher()
    expect(altchaMounted).toHaveBeenCalledTimes(1)
    expect(altchaMounted.mock.calls[0]?.[0]?.action).toBeUndefined()
    expect(turnstileMounted).not.toHaveBeenCalled()
    expect(recaptchaMounted).not.toHaveBeenCalled()
  })

  it('mounts the turnstile field with the site action', () => {
    setProvider(CaptchaProvider.TURNSTILE)
    renderDispatcher()
    expect(turnstileMounted).toHaveBeenCalledTimes(1)
    expect(turnstileMounted.mock.calls[0]?.[0]?.action).toBe('login')
  })

  it('mounts the recaptcha field for recaptcha_v3', () => {
    setProvider(CaptchaProvider.RECAPTCHA_V3)
    renderDispatcher()
    expect(recaptchaMounted).toHaveBeenCalledTimes(1)
    expect(recaptchaMounted.mock.calls[0]?.[0]?.action).toBe('login')
  })

  it('re-mounts when the provider signal flips (runtime switch without reload)', () => {
    const { container } = renderDispatcher()
    expect(altchaMounted).toHaveBeenCalledTimes(1)

    setProvider(CaptchaProvider.RECAPTCHA_V3)
    expect(container.querySelector('[data-testid="recaptcha"]')).toBeTruthy()
    expect(container.querySelector('[data-testid="altcha"]')).toBeFalsy()
  })

  it('falls back to altcha for an unrecognized enum value', () => {
    setProvider(99 as CaptchaProvider)
    renderDispatcher()
    expect(altchaMounted).toHaveBeenCalledTimes(1)
  })

  it('re-mounts the provider field when the form action changes', () => {
    setProvider(CaptchaProvider.TURNSTILE)
    const [action, setAction] = createSignal<CaptchaFieldProps['action']>('login')
    render(() => (
      <CaptchaField action={action()} onPayload={vi.fn()} onUnavailable={vi.fn()} ref={vi.fn()} />
    ))
    expect(turnstileMounted).toHaveBeenCalledTimes(1)
    expect(turnstileMounted.mock.calls[0]?.[0]?.action).toBe('login')

    setAction('passkey_login')
    expect(turnstileMounted).toHaveBeenCalledTimes(2)
    expect(turnstileMounted.mock.calls[1]?.[0]?.action).toBe('passkey_login')
  })
})
