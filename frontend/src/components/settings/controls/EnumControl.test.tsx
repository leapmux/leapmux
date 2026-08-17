import type { EnumOption } from './EnumControl'
import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { deferred, flush } from '~/test-support/async'
import { EnumControl } from './EnumControl'

afterEach(() => {
  cleanup()
})

// Five options, so the control takes the `<select>` branch — the only one
// that holds the choice in the DOM and therefore the only one that repairs a
// refused write. The pill branch re-derives every pill from `props.value`.
const CAPTCHA_PROVIDERS: EnumOption[] = [
  { value: 'none', label: 'None' },
  { value: 'altcha', label: 'ALTCHA' },
  { value: 'turnstile', label: 'Turnstile' },
  { value: 'hcaptcha', label: 'hCaptcha' },
  { value: 'recaptcha', label: 'reCAPTCHA' },
]

function combobox(name = 'Captcha provider'): HTMLSelectElement {
  return screen.getByRole('combobox', { name }) as HTMLSelectElement
}

describe('enumControl select branch', () => {
  it('commits the chosen option', () => {
    const onChange = vi.fn()
    render(() => (
      <EnumControl ariaLabel="Captcha provider" value="altcha" options={CAPTCHA_PROVIDERS} onChange={onChange} />
    ))
    fireEvent.change(combobox(), { target: { value: 'turnstile' } })
    expect(onChange).toHaveBeenCalledWith('turnstile')
  })

  /**
   * The repair reads the binding when the write ANSWERS, not when it is
   * issued.
   *
   * A refusal can arrive long after the choice, and the binding can move in
   * that window — another admin writes the same key, or the store refreshes.
   * A version that captured `props.value` before it issued the write passes
   * every test whose binding holds one value for the whole test; it puts an
   * option on screen here that no tier holds any more, and Solid never
   * corrects it, because the tracked expression did not change.
   */
  it('restores the option the binding holds at reply time, not at issue time', async () => {
    const refused = deferred<boolean>()
    const [stored, setStored] = createSignal('altcha')
    render(() => (
      <EnumControl
        ariaLabel="Captcha provider"
        value={stored()}
        options={CAPTCHA_PROVIDERS}
        onChange={() => refused.promise}
      />
    ))
    const select = combobox()

    fireEvent.change(select, { target: { value: 'recaptcha' } })
    setStored('turnstile')
    expect(select.value, 'the binding moved while the write was in flight').toBe('turnstile')

    refused.resolve(false)
    await flush()
    expect(select.value).toBe('turnstile')
  })

  it('keeps the chosen option when the write is accepted', async () => {
    const accepted = deferred<boolean>()
    render(() => (
      <EnumControl
        ariaLabel="Captcha provider"
        value="altcha"
        options={CAPTCHA_PROVIDERS}
        onChange={() => accepted.promise}
      />
    ))
    const select = combobox()

    fireEvent.change(select, { target: { value: 'turnstile' } })
    accepted.resolve(true)
    await flush()
    expect(select.value).toBe('turnstile')
  })

  // Only `false` means refused. A caller with nothing to report returns void,
  // and treating that as a refusal would put the old option back under an
  // accepted choice.
  it('leaves the chosen option alone when the write reports nothing', async () => {
    render(() => (
      <EnumControl ariaLabel="Captcha provider" value="altcha" options={CAPTCHA_PROVIDERS} onChange={() => {}} />
    ))
    const select = combobox()

    fireEvent.change(select, { target: { value: 'turnstile' } })
    await flush()
    expect(select.value).toBe('turnstile')
  })
})

describe('enumControl help line', () => {
  // The schema declares a help line per enum value and carries it over the
  // wire, but a pill and an `<option>` can each show a label only. One line
  // under the control serves both branches.
  it('follows the selection in the select branch', () => {
    const [stored, setStored] = createSignal('altcha')
    render(() => (
      <EnumControl
        ariaLabel="Captcha provider"
        value={stored()}
        options={[
          { value: 'none', label: 'None', help: 'No challenge at all.' },
          { value: 'altcha', label: 'ALTCHA', help: 'Self-hosted proof of work.' },
          { value: 'turnstile', label: 'Turnstile', help: 'Cloudflare Turnstile.' },
          { value: 'hcaptcha', label: 'hCaptcha' },
          { value: 'recaptcha', label: 'reCAPTCHA', help: 'Google reCAPTCHA.' },
        ]}
        onChange={vi.fn()}
      />
    ))
    expect(screen.getByText('Self-hosted proof of work.')).toBeTruthy()

    setStored('turnstile')
    expect(screen.getByText('Cloudflare Turnstile.')).toBeTruthy()
    expect(screen.queryByText('Self-hosted proof of work.')).toBeNull()

    // An option that declares none prints no empty line.
    setStored('hcaptcha')
    expect(screen.queryByText('Cloudflare Turnstile.')).toBeNull()
  })
})
