import type { EnumOption } from './EnumControl'
import { cleanup, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { deferred, flush } from '~/test-support/async'
import { menuOptions, menuTriggerText, pickMenuOption } from '~/test-support/menu'
import { EnumControl } from './EnumControl'

afterEach(() => {
  cleanup()
})

// Five options, so the control takes the MENU branch rather than the pills.
// Both branches now re-derive their selection from `props.value`, which is the
// point of the cases below: the DOM repair the `<select>` branch used to need
// is not merely unused, it is unreachable.
const CAPTCHA_PROVIDERS: EnumOption[] = [
  { value: 'none', label: 'None' },
  { value: 'altcha', label: 'ALTCHA' },
  { value: 'turnstile', label: 'Turnstile' },
  { value: 'hcaptcha', label: 'hCaptcha' },
  { value: 'recaptcha', label: 'reCAPTCHA' },
]

const MENU = 'enum-control-menu'

describe('enumControl menu branch', () => {
  it('commits the chosen option', () => {
    const onChange = vi.fn()
    render(() => (
      <EnumControl ariaLabel="Captcha provider" value="altcha" options={CAPTCHA_PROVIDERS} onChange={onChange} />
    ))
    pickMenuOption(MENU, 'Turnstile')
    expect(onChange).toHaveBeenCalledWith('turnstile')
  })

  /**
   * The trigger has four states, and `LoadingMenu` falls back to `emptyLabel`
   * for the empty value when no `placeholder` is given. Without one, a setting
   * that is genuinely unset -- a fresh install before the first write -- read
   * "No options" above a menu the user can see holds five, which states the
   * opposite of what is on screen.
   */
  it('prompts for a choice when the binding is empty, rather than claiming there are none', () => {
    render(() => (
      <EnumControl ariaLabel="Captcha provider" value="" options={CAPTCHA_PROVIDERS} onChange={vi.fn()} />
    ))
    expect(menuTriggerText(MENU)).toContain('Select an option')
    expect(menuTriggerText(MENU)).not.toContain('No options')
    // The prompt must not cost the user the list it prompts for.
    expect(menuOptions(MENU)).toEqual(CAPTCHA_PROVIDERS.map(o => o.label))
  })

  // An empty OPTION list is the state `emptyLabel` answers for, and the
  // placeholder must not take that case over. Zero options is fewer than
  // PILL_MAX_OPTIONS, so this case took the PILL branch and `PillGroup` drew a
  // blank row -- which also left `isEmpty` and the required `emptyLabel`
  // unreachable, so nothing on screen said the list was empty.
  it('still says there are none when the list is genuinely empty', () => {
    render(() => <EnumControl ariaLabel="Captcha provider" value="" options={[]} onChange={vi.fn()} />)
    expect(menuTriggerText(MENU)).toContain('No options')
    expect(screen.getByTestId(`${MENU}-trigger`)).toBeDisabled()
  })

  it('offers every option, and checks the one the binding holds', () => {
    render(() => (
      <EnumControl ariaLabel="Captcha provider" value="turnstile" options={CAPTCHA_PROVIDERS} onChange={vi.fn()} />
    ))
    expect(menuOptions(MENU)).toEqual(CAPTCHA_PROVIDERS.map(o => o.label))
    expect(menuTriggerText(MENU)).toContain('Turnstile')
  })

  /**
   * The DOM repair this branch used to need is now unreachable.
   *
   * A `<select>` held the choice in `selectedIndex`, so a refused write left the
   * rejected option on screen and the handler had to put the stored one back --
   * reading the binding at REPLY time, because a refusal can arrive long after
   * the choice and the binding can move in between. A menu shows whatever
   * `props.value` holds on every render, so a refused write never moved the
   * display in the first place and there is nothing to restore.
   */
  it('shows the binding, not the pending choice, while a write is in flight', async () => {
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

    pickMenuOption(MENU, 'reCAPTCHA')
    // Never showed reCAPTCHA: the display follows the binding, which still
    // holds altcha.
    expect(menuTriggerText(MENU)).toContain('ALTCHA')

    // The binding moves under the in-flight write, and the display follows it.
    setStored('turnstile')
    expect(menuTriggerText(MENU)).toContain('Turnstile')

    refused.resolve(false)
    await flush()
    expect(menuTriggerText(MENU)).toContain('Turnstile')
  })

  it('shows the accepted option once the binding carries it', async () => {
    const accepted = deferred<boolean>()
    const [stored, setStored] = createSignal('altcha')
    render(() => (
      <EnumControl
        ariaLabel="Captcha provider"
        value={stored()}
        options={CAPTCHA_PROVIDERS}
        onChange={() => {
          setStored('turnstile')
          return accepted.promise
        }}
      />
    ))

    pickMenuOption(MENU, 'Turnstile')
    accepted.resolve(true)
    await flush()
    expect(menuTriggerText(MENU)).toContain('Turnstile')
  })
})

describe('enumControl help line', () => {
  // The schema declares a help line per enum value and carries it over the
  // wire, but a pill and a menu item each show a label only. One line under the
  // control serves both branches.
  it('follows the selection in the menu branch', () => {
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
