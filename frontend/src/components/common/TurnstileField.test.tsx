import type { CaptchaFieldHandle } from './CaptchaField'
/// <reference types="vitest/globals" />
import { render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mockLoadSystemInfo, resetSystemInfoMock, setSystemInfoMock } from '~/test-support/systemInfoMock'
import { TurnstileField } from './TurnstileField'

// The script loader is stubbed so the component tests drive only the
// turnstile SDK seam.
const mockLoadScript = vi.fn(() => Promise.resolve())
vi.mock('~/lib/scriptLoader', () => ({
  loadExternalScript: (...args: []) => mockLoadScript(...args),
}))

// The site key reads through the shared mock's real Solid signal so the
// component's reactive tracking sees a rotation the way the production
// module's snapshot signal delivers it.
vi.mock('~/lib/systemInfo', async () => {
  const m = await import('~/test-support/systemInfoMock')
  return m.systemInfoMock
})

interface RenderCall {
  container: HTMLElement
  options: {
    'sitekey': string
    'action'?: string
    'callback'?: (token: string) => void
    'expired-callback'?: () => void
    'error-callback'?: (code: number | string) => void
  }
}

function installFakeTurnstile() {
  const calls: RenderCall[] = []
  const resets: (string | undefined)[] = []
  const removes: (string | undefined)[] = []
  window.turnstile = {
    render: (container, options) => {
      const el = typeof container === 'string'
        ? document.querySelector<HTMLElement>(container)!
        : container
      calls.push({ container: el, options })
      return `widget-${calls.length}`
    },
    reset: (id?: string) => resets.push(id),
    remove: (id?: string) => removes.push(id),
    getResponse: () => undefined,
    ready: (cb: () => void) => cb(),
  }
  return { calls, resets, removes }
}

function renderField(props: Partial<Parameters<typeof TurnstileField>[0]> = {}) {
  const onPayload = props.onPayload ?? vi.fn()
  return render(() => (
    <div><TurnstileField action={props.action ?? 'login'} onPayload={onPayload} {...props} /></div>
  ))
}

describe('turnstileField', () => {
  beforeEach(() => {
    mockLoadScript.mockReset()
    mockLoadScript.mockResolvedValue(undefined)
    resetSystemInfoMock()
    setSystemInfoMock({ captchaSiteKey: '1x00000000000000000000AA' })
    delete window.turnstile
  })

  it('renders explicitly with the hub site key and the form action', async () => {
    const turnstile = installFakeTurnstile()
    renderField({ action: 'signup' })
    await vi.waitFor(() => {
      expect(turnstile.calls).toHaveLength(1)
    })
    expect(turnstile.calls[0]?.container).toBeInstanceOf(HTMLElement)
    expect(turnstile.calls[0]?.options.sitekey).toBe('1x00000000000000000000AA')
    expect(turnstile.calls[0]?.options.action).toBe('signup')
    expect(mockLoadScript).toHaveBeenCalledWith('https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit')
  })

  it('delivers the callback token and nulls it on expiry', async () => {
    const turnstile = installFakeTurnstile()
    const onPayload = vi.fn()
    renderField({ onPayload })
    await vi.waitFor(() => {
      expect(turnstile.calls).toHaveLength(1)
    })

    turnstile.calls[0]?.options.callback?.('tok0')
    expect(onPayload).toHaveBeenLastCalledWith('tok0')

    // A dead token must never be submitted; the widget refreshes itself
    // and the next callback re-arms the form.
    turnstile.calls[0]?.options['expired-callback']?.()
    expect(onPayload).toHaveBeenLastCalledWith(null)
  })

  it('surfaces widget errors but never stands down', async () => {
    const turnstile = installFakeTurnstile()
    const onPayload = vi.fn()
    const { findByText } = renderField({ onPayload })
    await vi.waitFor(() => {
      expect(turnstile.calls).toHaveLength(1)
    })

    turnstile.calls[0]?.options.callback?.('tok0')
    turnstile.calls[0]?.options['error-callback']?.(300100)
    expect(onPayload).toHaveBeenLastCalledWith(null)
    expect(await findByText(/could not load the human-verification challenge/i)).toBeTruthy()

    // The widget retries by itself; the next token clears the error.
    turnstile.calls[0]?.options.callback?.('tok1')
    expect(onPayload).toHaveBeenLastCalledWith('tok1')
  })

  it('refreshes the system info when the widget errors, so a stale site key converges', async () => {
    // The widget's unrecoverable errors are typically a site key the
    // snapshot no longer knows; with the payload dropped the disabled
    // submit button can never trigger the denial-driven refresh, so the
    // field's own error path must — the external providers' counterpart
    // of the altcha fetch-failure convergence.
    const turnstile = installFakeTurnstile()
    renderField()
    await vi.waitFor(() => {
      expect(turnstile.calls).toHaveLength(1)
    })

    turnstile.calls[0]?.options['error-callback']?.(300100)
    expect(mockLoadSystemInfo).toHaveBeenCalledWith(true)
  })

  it('reset handle nulls the payload and re-runs the challenge', async () => {
    const turnstile = installFakeTurnstile()
    let handle: CaptchaFieldHandle | undefined
    const onPayload = vi.fn()
    render(() => (
      <div><TurnstileField action="login" onPayload={onPayload} ref={h => (handle = h)} /></div>
    ))
    await vi.waitFor(() => {
      expect(turnstile.calls).toHaveLength(1)
    })

    turnstile.calls[0]?.options.callback?.('tok0')
    handle!.reset()
    expect(onPayload).toHaveBeenLastCalledWith(null)
    expect(turnstile.resets).toEqual(['widget-1'])
  })

  it('shows a load error when the script fails to arrive', async () => {
    mockLoadScript.mockRejectedValue(new Error('network'))
    installFakeTurnstile()
    const { findByText } = renderField()
    expect(await findByText(/could not load the human-verification challenge/i)).toBeTruthy()
  })

  it('removes the widget on cleanup and drops the payload', async () => {
    const turnstile = installFakeTurnstile()
    const onPayload = vi.fn()
    const { unmount } = renderField({ onPayload })
    await vi.waitFor(() => {
      expect(turnstile.calls).toHaveLength(1)
    })

    unmount()
    expect(turnstile.removes).toEqual(['widget-1'])
    expect(onPayload).toHaveBeenLastCalledWith(null)
  })

  it('re-renders the widget under the new key after a site-key rotation', async () => {
    const turnstile = installFakeTurnstile()
    const onPayload = vi.fn()
    renderField({ onPayload })
    await vi.waitFor(() => {
      expect(turnstile.calls).toHaveLength(1)
    })
    turnstile.calls[0]?.options.callback?.('tok-under-old-key')

    // The admin rotated the keys; the denial-driven reload updated the
    // signal. Tokens under the retired key always fail siteverify, so the
    // widget must re-render under the new key, not reset under the old.
    setSystemInfoMock({ captchaSiteKey: '2x00000000000000000000BB' })
    await vi.waitFor(() => {
      expect(turnstile.calls).toHaveLength(2)
    })
    expect(turnstile.removes).toEqual(['widget-1'])
    expect(turnstile.calls[1]?.options.sitekey).toBe('2x00000000000000000000BB')
    expect(onPayload).toHaveBeenLastCalledWith(null)
  })

  it('re-renders the widget when the form action changes', async () => {
    const turnstile = installFakeTurnstile()
    const onPayload = vi.fn()
    const [action, setAction] = createSignal('login')
    render(() => (
      <div><TurnstileField action={action()} onPayload={onPayload} /></div>
    ))
    await vi.waitFor(() => {
      expect(turnstile.calls).toHaveLength(1)
    })
    expect(turnstile.calls[0]?.options.action).toBe('login')

    setAction('passkey_login')
    await vi.waitFor(() => {
      expect(turnstile.calls).toHaveLength(2)
    })
    expect(turnstile.calls[1]?.options.action).toBe('passkey_login')
    expect(turnstile.removes).toEqual(['widget-1'])
    expect(onPayload).toHaveBeenLastCalledWith(null)
  })
})
