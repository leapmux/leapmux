import type { TurnstileFieldHandle } from './TurnstileField'
/// <reference types="vitest/globals" />
import { render } from '@solidjs/testing-library'

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { TurnstileField } from './TurnstileField'

// The script loader is stubbed so the component tests drive only the
// turnstile SDK seam.
const mockLoadScript = vi.fn(() => Promise.resolve())
vi.mock('~/lib/scriptLoader', () => ({
  loadExternalScript: (...args: []) => mockLoadScript(...args),
}))

vi.mock('~/lib/systemInfo', () => ({
  getCaptchaSiteKey: () => '1x00000000000000000000AA',
}))

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

  it('reset handle nulls the payload and re-runs the challenge', async () => {
    const turnstile = installFakeTurnstile()
    let handle: TurnstileFieldHandle | undefined
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
})
