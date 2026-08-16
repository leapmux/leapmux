import type { CaptchaFieldHandle } from './CaptchaField'
/// <reference types="vitest/globals" />
import { render } from '@solidjs/testing-library'

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mockLoadSystemInfo, resetSystemInfoMock } from '~/test-support/systemInfoMock'
import { AltchaField } from './AltchaField'
import { CaptchaHoneypot } from './CaptchaHoneypot'

// Neutralize the real altcha module (custom-element registration + CSS
// injection) and register a stub element instead, so tests drive the
// widget seam directly: configure/reset spies and manual statechange
// dispatches.
vi.mock('altcha', () => ({}))

// The data half (fetch + parse + solver pre-warm) has its own seam in
// altchaChallenge.test.ts; here it is a controllable stand-in so the
// component tests drive the widget lifecycle only.
const mockFetchAltchaChallenge = vi.fn()
vi.mock('~/lib/altchaChallenge', () => ({
  fetchAltchaChallenge: (...args: []) => mockFetchAltchaChallenge(...args),
}))

// The catch path's convergence seam: a failed fetch must force one
// system-info refresh so a runtime provider switch re-mounts the right
// field instead of dead-ending behind the disabled submit button. The
// shared systemInfo mock keeps loadSystemInfo as the assertion seam.
vi.mock('~/lib/systemInfo', async () => {
  const m = await import('~/test-support/systemInfoMock')
  return m.systemInfoMock
})

class FakeAltchaWidget extends HTMLElement {
  configure = vi.fn((_config: unknown) => Promise.resolve())
  reset = vi.fn()
}
if (!customElements.get('altcha-widget')) {
  customElements.define('altcha-widget', FakeAltchaWidget)
}

const challenge = { parameters: { algorithm: 'PBKDF2/SHA-256', salt: 'abc', cost: 10000 }, signature: 'sig' }

function widgetEls(container: HTMLElement): FakeAltchaWidget[] {
  return Array.from(container.querySelectorAll('altcha-widget')) as unknown as FakeAltchaWidget[]
}

function renderField(props: Partial<Parameters<typeof AltchaField>[0]> = {}) {
  const onPayload = props.onPayload ?? vi.fn()
  const onUnavailable = props.onUnavailable ?? vi.fn()
  return render(() => (
    <div><AltchaField onPayload={onPayload} onUnavailable={onUnavailable} {...props} /></div>
  ))
}

describe('altchaField', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    resetSystemInfoMock()
    mockFetchAltchaChallenge.mockResolvedValue(challenge)
  })

  it('fetches a challenge on mount and configures the widget with the parsed object', async () => {
    const { container } = renderField()
    await vi.waitFor(() => {
      expect(widgetEls(container)[0].configure).toHaveBeenCalled()
    })
    const arg = widgetEls(container)[0].configure.mock.calls[0]?.[0] as
      | { challenge?: { parameters?: { algorithm?: string } }, auto?: string }
      | undefined
    // The challenge must be handed over as an object — a raw string would
    // be treated as a fetch URL by the widget.
    expect(arg).toBeDefined()
    expect(typeof arg?.challenge).toBe('object')
    expect(arg?.challenge?.parameters?.algorithm).toBe('PBKDF2/SHA-256')
    expect(arg?.auto).toBe('off')
  })

  it('stands down (no configure, no error) when the hub reports no challenge', async () => {
    mockFetchAltchaChallenge.mockResolvedValue(null)
    const onUnavailable = vi.fn()
    const { container } = renderField({ onUnavailable })
    await vi.waitFor(() => {
      expect(onUnavailable).toHaveBeenCalled()
    })
    expect(widgetEls(container)[0].configure).not.toHaveBeenCalled()
  })

  it('emits the payload on verified statechange and null otherwise', async () => {
    const onPayload = vi.fn()
    const { container } = renderField({ onPayload })
    await vi.waitFor(() => {
      expect(widgetEls(container)[0].configure).toHaveBeenCalled()
    })
    const widget = widgetEls(container)[0]

    widget.dispatchEvent(new CustomEvent('statechange', {
      detail: { state: 'verified', payload: 'cGF5bG9hZA==' },
    }))
    expect(onPayload).toHaveBeenCalledWith('cGF5bG9hZA==')

    widget.dispatchEvent(new CustomEvent('statechange', { detail: { state: 'unverified' } }))
    expect(onPayload).toHaveBeenLastCalledWith(null)
  })

  it('re-arms on expired statechange', async () => {
    const { container } = renderField()
    await vi.waitFor(() => {
      expect(widgetEls(container)[0].configure).toHaveBeenCalled()
    })
    const widget = widgetEls(container)[0]

    widget.dispatchEvent(new CustomEvent('statechange', { detail: { state: 'expired' } }))
    await vi.waitFor(() => {
      expect(mockFetchAltchaChallenge).toHaveBeenCalledTimes(2)
    })
  })

  it('shows a load error when the challenge fetch fails', async () => {
    mockFetchAltchaChallenge.mockRejectedValue(new Error('network'))
    const { container, findByText } = renderField()
    expect(await findByText(/could not load the human-verification challenge/i)).toBeTruthy()
    expect(widgetEls(container)[0].configure).not.toHaveBeenCalled()
  })

  it('refreshes the system info once when the challenge fetch fails, so a provider switch converges without a denial', async () => {
    // The hub answers FailedPrecondition after a runtime provider switch:
    // the fetch rejects, and the disabled submit button can never trigger
    // the denial-driven reload — the field's error path must do it.
    mockFetchAltchaChallenge.mockRejectedValue(new Error('FailedPrecondition'))
    const { findByText } = renderField()
    expect(await findByText(/could not load the human-verification challenge/i)).toBeTruthy()
    await vi.waitFor(() => {
      expect(mockLoadSystemInfo).toHaveBeenCalledWith(true)
    })
    expect(mockLoadSystemInfo).toHaveBeenCalledTimes(1)
  })

  it('ignores a stale overlapping arm: the newer challenge wins', async () => {
    // arm #1 hangs; arm #2 (a reset) resolves first. The late #1 response
    // must not overwrite the widget's newer challenge.
    let releaseFirst: (value: typeof challenge) => void = () => {}
    const first = new Promise<typeof challenge>(resolve => (releaseFirst = resolve))
    let call = 0
    mockFetchAltchaChallenge.mockImplementation(() => {
      call++
      return call === 1 ? first : Promise.resolve(challenge)
    })
    let handle: CaptchaFieldHandle | undefined
    const { container } = render(() => (
      <div><AltchaField onPayload={vi.fn()} onUnavailable={vi.fn()} ref={h => (handle = h)} /></div>
    ))

    await vi.waitFor(() => {
      expect(mockFetchAltchaChallenge).toHaveBeenCalledTimes(1)
    })
    handle!.reset()
    await vi.waitFor(() => {
      expect(mockFetchAltchaChallenge).toHaveBeenCalledTimes(2)
      expect(widgetEls(container)[0].configure).toHaveBeenCalledTimes(1)
    })

    // The hung first fetch resolves late; only the newer arm may configure.
    releaseFirst(challenge)
    await new Promise(r => setTimeout(r, 0))
    expect(widgetEls(container)[0].configure).toHaveBeenCalledTimes(1)
  })

  it('reset handle clears the payload and fetches a fresh challenge', async () => {
    let handle: CaptchaFieldHandle | undefined
    const { container } = render(() => (
      <div><AltchaField onPayload={vi.fn()} onUnavailable={vi.fn()} ref={h => (handle = h)} /></div>
    ))
    await vi.waitFor(() => {
      expect(widgetEls(container)[0].configure).toHaveBeenCalled()
    })

    handle!.reset()
    expect(widgetEls(container)[0].reset).toHaveBeenCalled()
    await vi.waitFor(() => {
      expect(mockFetchAltchaChallenge).toHaveBeenCalledTimes(2)
    })
  })
})

describe('captchaHoneypot', () => {
  it('renders the input hidden from users but visible to bots, and reports input', () => {
    const onInput = vi.fn()
    const { container } = render(() => (
      <div><CaptchaHoneypot value="" onInput={onInput} /></div>
    ))
    const honeypot = container.querySelector<HTMLInputElement>('input[name="website"]')!
    expect(honeypot.tabIndex).toBe(-1)
    expect(honeypot.getAttribute('aria-hidden')).toBe('true')
    expect(honeypot.autocomplete).toBe('off')

    honeypot.value = 'http://spam.example'
    honeypot.dispatchEvent(new Event('input', { bubbles: true }))
    expect(onInput).toHaveBeenCalledWith('http://spam.example')
  })

  it('clears the DOM value when the controlled value resets', async () => {
    const { createSignal } = await import('solid-js')
    const onInput = vi.fn()
    const [value, setValue] = createSignal('stale')
    const { container } = render(() => (
      <div><CaptchaHoneypot value={value()} onInput={onInput} /></div>
    ))
    const honeypot = container.querySelector<HTMLInputElement>('input[name="website"]')!
    expect(honeypot.value).toBe('stale')

    // A reset that clears the signal clears the field an autofill
    // heuristic populated.
    setValue('')
    await vi.waitFor(() => {
      expect(container.querySelector<HTMLInputElement>('input[name="website"]')!.value).toBe('')
    })
  })
})
