import type { SettingBinding, SettingDescriptor } from './types'
import { cleanup, fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { formatEffectiveValue, SettingRow } from './SettingRow'

function descriptor(overrides: Partial<SettingDescriptor> = {}): SettingDescriptor {
  return {
    id: 'test.row',
    category: 'appearance',
    label: 'Test row',
    scope: 'browser',
    control: { kind: 'toggle' },
    ...overrides,
  }
}

function binding(overrides: Partial<SettingBinding> = {}): SettingBinding {
  return {
    value: () => false,
    set: vi.fn(),
    ...overrides,
  }
}

afterEach(() => {
  cleanup()
})

describe('settingRow control kinds', () => {
  it('renders a radiogroup for enum controls and commits the choice', async () => {
    const set = vi.fn()
    render(() => (
      <SettingRow
        descriptor={descriptor({ control: { kind: 'enum', options: [
          { value: 'dark', label: 'Dark' },
          { value: 'light', label: 'Light' },
        ] } })}
        binding={binding({ value: () => 'dark', set })}
      />
    ))
    fireEvent.click(screen.getByRole('radio', { name: 'Light' }))
    await vi.waitFor(() => expect(set).toHaveBeenCalledWith('light'))
  })

  it('renders a native select for wide enums', () => {
    render(() => (
      <SettingRow
        descriptor={descriptor({ control: { kind: 'enum', options: [
          { value: 'a', label: 'A' },
          { value: 'b', label: 'B' },
          { value: 'c', label: 'C' },
          { value: 'd', label: 'D' },
          { value: 'e', label: 'E' },
        ] } })}
        binding={binding({ value: () => 'a' })}
      />
    ))
    expect(screen.getByRole('combobox')).toBeTruthy()
  })

  it('renders a switch for toggles and commits flips', async () => {
    const set = vi.fn()
    render(() => <SettingRow descriptor={descriptor()} binding={binding({ set })} />)
    fireEvent.click(screen.getByRole('switch', { name: 'Test row' }))
    await vi.waitFor(() => expect(set).toHaveBeenCalledWith(true))
  })

  it('renders a slider with a readout for slider controls', async () => {
    const set = vi.fn()
    render(() => (
      <SettingRow
        descriptor={descriptor({ control: { kind: 'slider', min: 0, max: 100, step: 1, unit: '%' } })}
        binding={binding({ value: () => 40, set })}
      />
    ))
    const slider = screen.getByRole('slider', { name: 'Test row' }) as HTMLInputElement
    expect(slider.value).toBe('40')
    expect(screen.getByText('40%')).toBeTruthy()
    fireEvent.input(slider, { target: { value: '60' } })
    fireEvent.change(slider, { target: { value: '60' } })
    await vi.waitFor(() => expect(set).toHaveBeenCalledWith(60))
  })

  it('renders an action ConfirmButton that commits true on the second click', async () => {
    const set = vi.fn()
    render(() => (
      <SettingRow
        descriptor={descriptor({ control: { kind: 'action', label: 'Reset overrides', danger: true } })}
        binding={binding({ set })}
      />
    ))
    const button = screen.getByTestId('setting-action-test.row')
    expect(button).toHaveAttribute('data-variant', 'danger')
    fireEvent.click(button)
    expect(set).not.toHaveBeenCalled()
    expect(button).toHaveTextContent('Confirm?')
    fireEvent.click(button)
    await vi.waitFor(() => expect(set).toHaveBeenCalledWith(true))
  })

  it('renders a number input with its unit as adjacent text', async () => {
    const set = vi.fn()
    render(() => (
      <SettingRow
        descriptor={descriptor({ control: { kind: 'number', min: 300, step: 1, unit: 'seconds' } })}
        binding={binding({ value: () => 604800, set })}
      />
    ))
    const input = screen.getByRole('spinbutton', { name: 'Test row' }) as HTMLInputElement
    expect(input.value).toBe('604800')
    expect(screen.getByText('seconds')).toBeTruthy()
    fireEvent.change(input, { target: { value: '3600' } })
    await vi.waitFor(() => expect(set).toHaveBeenCalledWith(3600))
  })

  it('keeps an unset number field empty instead of coercing to 0', () => {
    render(() => (
      <SettingRow
        descriptor={descriptor({ control: { kind: 'number', unit: 'bytes' } })}
        binding={binding({ value: () => undefined })}
      />
    ))
    const input = screen.getByRole('spinbutton', { name: 'Test row' }) as HTMLInputElement
    expect(input.value).toBe('')
  })

  // The backend schema declares a help line per enum value (each SMTP TLS
  // mode, each captcha provider) and puts it on the wire. A pill and an
  // `<option>` can each carry a label only, so without a line under the
  // control every declared explanation was silently discarded.
  it('renders the selected enum option help, and follows the selection', () => {
    const [value, setValue] = createSignal('starttls')
    render(() => (
      <SettingRow
        descriptor={descriptor({ control: { kind: 'enum', options: [
          { value: 'starttls', label: 'STARTTLS', help: 'Upgrade to TLS on port 587.' },
          { value: 'implicit', label: 'Implicit TLS', help: 'Dial TLS directly on port 465.' },
          { value: 'none', label: 'None' },
        ] } })}
        binding={binding({ value, set: (v) => { setValue(v as string) } })}
      />
    ))
    expect(screen.getByText('Upgrade to TLS on port 587.')).toBeTruthy()

    setValue('implicit')
    expect(screen.getByText('Dial TLS directly on port 465.')).toBeTruthy()
    expect(screen.queryByText('Upgrade to TLS on port 587.')).toBeNull()

    // An option that declares no help renders no line.
    setValue('none')
    expect(screen.queryByText('Dial TLS directly on port 465.')).toBeNull()
  })

  it('renders a text input with placeholder and commits on change', async () => {
    const set = vi.fn()
    render(() => (
      <SettingRow
        descriptor={descriptor({ control: { kind: 'text', placeholder: 'e.g. vscode' } })}
        binding={binding({ value: () => 'zed', set })}
      />
    ))
    const input = screen.getByRole('textbox', { name: 'Test row' }) as HTMLInputElement
    expect(input.placeholder).toBe('e.g. vscode')
    fireEvent.change(input, { target: { value: 'vscode' } })
    await vi.waitFor(() => expect(set).toHaveBeenCalledWith('vscode'))
  })

  // Every commit is one write RPC. A per-keystroke commit stored each prefix
  // of what the user typed, so a half-typed public base URL became the hub's
  // real one for as long as it took to type the rest.
  it('does not commit a text input on every keystroke', () => {
    const set = vi.fn()
    render(() => (
      <SettingRow
        descriptor={descriptor({ control: { kind: 'text' } })}
        binding={binding({ value: () => '', set })}
      />
    ))
    const input = screen.getByRole('textbox', { name: 'Test row' }) as HTMLInputElement
    fireEvent.input(input, { target: { value: 'h' } })
    fireEvent.input(input, { target: { value: 'ht' } })
    fireEvent.input(input, { target: { value: 'htt' } })
    expect(set).not.toHaveBeenCalled()
    fireEvent.change(input, { target: { value: 'https://hub.example.com' } })
    expect(set).toHaveBeenCalledTimes(1)
    expect(set).toHaveBeenCalledWith('https://hub.example.com')
  })

  // `Number('')` is 0, and 0 is a real setting for several keys — a per-user
  // cap of 0 means unlimited, a queue budget of 0 means auto-size — so a
  // cleared field must commit nothing rather than remove the cap.
  it('commits a typed zero but not a cleared number field', () => {
    const set = vi.fn()
    render(() => (
      <SettingRow
        descriptor={descriptor({ control: { kind: 'number', min: 0, max: 100 } })}
        binding={binding({ value: () => 25, set })}
      />
    ))
    const input = screen.getByRole('spinbutton', { name: 'Test row' }) as HTMLInputElement
    fireEvent.change(input, { target: { value: '' } })
    expect(set).not.toHaveBeenCalled()
    // And the field must not keep showing the blank it never committed.
    // Solid assigns `value` only when the tracked expression CHANGES, and
    // the stored value did not — so nothing else puts the number back, and
    // the row would show an empty box over a stored 25 with no error.
    expect(input.value).toBe('25')
    fireEvent.change(input, { target: { value: '0' } })
    expect(set).toHaveBeenCalledTimes(1)
    expect(set).toHaveBeenCalledWith(0)
  })

  it('renders a write-only secret with Set/Replace from isSet, never a stored value', async () => {
    const set = vi.fn(async () => {})
    render(() => (
      <SettingRow
        descriptor={descriptor({ control: { kind: 'secret', isSet: () => false } })}
        binding={binding({ set })}
      />
    ))
    expect(screen.getByRole('button', { name: 'Set' })).toBeTruthy()
    const input = screen.getByLabelText('Test row') as HTMLInputElement
    expect(input.type).toBe('password')
    expect(input.autocomplete).toBe('new-password')
    fireEvent.input(input, { target: { value: 'hunter2' } })
    fireEvent.click(screen.getByRole('button', { name: 'Set' }))
    await vi.waitFor(() => expect(set).toHaveBeenCalledWith('hunter2'))
  })

  it('labels the secret button Replace when a value is already stored', () => {
    render(() => (
      <SettingRow
        descriptor={descriptor({ control: { kind: 'secret', isSet: () => true } })}
        binding={binding()}
      />
    ))
    expect(screen.getByRole('button', { name: 'Replace' })).toBeTruthy()
  })

  it('renders the string list editor', async () => {
    const set = vi.fn()
    render(() => (
      <SettingRow
        descriptor={descriptor({ control: { kind: 'stringList', addLabel: 'Add font' } })}
        binding={binding({ value: () => ['Inter'], set })}
      />
    ))
    expect(screen.getByText('Inter')).toBeTruthy()
    const add = screen.getByRole('textbox', { name: 'Add font' })
    fireEvent.input(add, { target: { value: 'Hack NF' } })
    fireEvent.keyDown(add, { key: 'Enter' })
    await vi.waitFor(() => expect(set).toHaveBeenCalledWith(['Inter', 'Hack NF']))
  })
})

describe('settingRow status area', () => {
  it('shows Customized + Reset on customized rows and calls reset', async () => {
    const reset = vi.fn(async () => {})
    render(() => (
      <SettingRow
        descriptor={descriptor({ scope: 'hub' })}
        binding={binding({ customized: () => true, reset })}
      />
    ))
    fireEvent.click(screen.getByTestId('setting-reset-test.row'))
    await vi.waitFor(() => expect(reset).toHaveBeenCalledOnce())
  })

  it('shows a Requires Restart badge for restart descriptors', () => {
    render(() => (
      <SettingRow
        descriptor={descriptor({ scope: 'hub', restart: true })}
        binding={binding()}
      />
    ))
    expect(screen.getByText('Requires Restart')).toBeTruthy()
  })

  /**
   * The two halves say DIFFERENT things: the control carries the
   * configured value the admin edits, the note carries what the hub
   * enforces. Assert both in one test, because the note alone passes just
   * as well when the control repeats it.
   *
   * The note speaks the CONTROL's vocabulary: a switch is on or off, and
   * the JSON literal `false` beneath a switch that reads ON asked the
   * reader to translate.
   */
  it('notes the enforced value beside the configured one the control shows', () => {
    render(() => (
      <SettingRow
        descriptor={descriptor()}
        binding={binding({ value: () => true, effective: () => false })}
      />
    ))
    expect(screen.getByRole<HTMLInputElement>('switch', { name: 'Test row' }).checked).toBe(true)
    expect(screen.getByText(/Currently in effect:\s*Off/)).toBeTruthy()
    expect(screen.queryByText(/Currently in effect:\s*false/)).toBeNull()
  })

  // The production case the note exists for: dev mode holds sign-up open
  // while the stored default is closed.
  it('notes an enforced on beside a configured off toggle', () => {
    render(() => (
      <SettingRow
        descriptor={descriptor()}
        binding={binding({ value: () => false, effective: () => true })}
      />
    ))
    expect(screen.getByRole<HTMLInputElement>('switch', { name: 'Test row' }).checked).toBe(false)
    expect(screen.getByText(/Currently in effect:\s*On/)).toBeTruthy()
  })

  // An enum control shows the option LABEL, never the wire value, so the
  // note beside it must show the label too. The captcha selection degrades
  // at read time, which is exactly this row.
  it('notes an enforced enum by its option label', () => {
    render(() => (
      <SettingRow
        descriptor={descriptor({ control: { kind: 'enum', options: [
          { value: 'altcha', label: 'ALTCHA' },
          { value: 'turnstile', label: 'Cloudflare Turnstile' },
        ] } })}
        binding={binding({ value: () => 'turnstile', effective: () => 'altcha' })}
      />
    ))
    expect(screen.getByRole('radio', { name: 'Cloudflare Turnstile' }).getAttribute('aria-checked')).toBe('true')
    expect(screen.getByText(/Currently in effect:\s*ALTCHA/)).toBeTruthy()
  })

  // An enforced `false` or `0` is a real value, and a falsy-guarded note
  // would drop exactly the case the note exists for. This key declares no
  // unit, so the figure stands alone.
  it('notes an enforced 0', () => {
    render(() => (
      <SettingRow
        descriptor={descriptor({ control: { kind: 'number', min: 0 } })}
        binding={binding({ value: () => 1024, effective: () => 0 })}
      />
    ))
    expect(screen.getByText(/Currently in effect:\s*0/)).toBeTruthy()
  })

  // The queue budget, which is why the note exists on a number row: the
  // operator configures 0 to auto-size, and the hub reports the byte count
  // it settled on. The unit belongs to the note for the same reason it
  // belongs beside the input.
  it('notes an enforced byte count with the unit its control shows', () => {
    render(() => (
      <SettingRow
        descriptor={descriptor({ control: { kind: 'number', min: 0, unit: 'bytes' } })}
        binding={binding({ value: () => 0, effective: () => 268435456 })}
      />
    ))
    expect(screen.getByText(/Currently in effect:\s*268435456 bytes/)).toBeTruthy()
  })

  // The unit guard tests the UNIT, never the value, so a falsy 0 keeps it.
  it('keeps the unit on an enforced 0', () => {
    render(() => (
      <SettingRow
        descriptor={descriptor({ control: { kind: 'number', min: 0, unit: 'bytes' } })}
        binding={binding({ value: () => 1024, effective: () => 0 })}
      />
    ))
    expect(screen.getByText(/Currently in effect:\s*0 bytes/)).toBeTruthy()
  })

  // A slider's readout writes `50%`, with nothing between the figure and
  // the sign, and the note beneath it reads the same way.
  it('notes an enforced percentage the way the slider readout writes it', () => {
    render(() => (
      <SettingRow
        descriptor={descriptor({ control: { kind: 'slider', min: 0, max: 100, step: 1, unit: '%' } })}
        binding={binding({ value: () => 20, effective: () => 50 })}
      />
    ))
    expect(screen.getByText(/Currently in effect:\s*50%/)).toBeTruthy()
  })

  it('prints no note when the binding reports no override', () => {
    render(() => (
      <SettingRow
        descriptor={descriptor()}
        binding={binding({ value: () => true, effective: () => undefined })}
      />
    ))
    expect(screen.queryByText(/Currently in effect/)).toBeNull()
  })

  it('renders an inline error', () => {
    render(() => <SettingRow descriptor={descriptor()} binding={binding()} error="boom" />)
    expect(screen.getByTestId('setting-error-test.row').textContent).toContain('boom')
  })

  it('surfaces a failed set as the row error', async () => {
    render(() => (
      <SettingRow
        descriptor={descriptor()}
        binding={binding({ set: () => Promise.reject(new Error('write failed')) })}
      />
    ))
    fireEvent.click(screen.getByRole('switch', { name: 'Test row' }))
    await vi.waitFor(() => expect(screen.getByTestId('setting-error-test.row').textContent).toContain('write failed'))
  })

  it('surfaces a failed reset as the row error', async () => {
    render(() => (
      <SettingRow
        descriptor={descriptor({ scope: 'hub' })}
        binding={binding({
          customized: () => true,
          reset: () => Promise.reject(new Error('reset failed')),
        })}
      />
    ))
    fireEvent.click(screen.getByTestId('setting-reset-test.row'))
    await vi.waitFor(() => expect(screen.getByTestId('setting-error-test.row').textContent).toContain('reset failed'))
  })
})

describe('settingRow scope chip', () => {
  it('dual rows open the tier menu and switch tiers', async () => {
    const clearOverride = vi.fn()
    const beginOverride = vi.fn()
    render(() => (
      <SettingRow
        descriptor={descriptor({ scope: 'dual' })}
        binding={binding({ overridden: () => false, clearOverride, beginOverride })}
      />
    ))
    const chip = screen.getByTestId('scope-chip-test.row')
    expect(chip.textContent).toContain('Account default')
    expect(chip.querySelector('svg')).toBeTruthy()

    fireEvent.click(chip)
    // `hidden: true`: the popover's not-yet-open content is display:none under
    // jsdom's UA sheet, which role queries filter out by default.
    fireEvent.click(screen.getByRole('menuitemradio', { name: 'Override on this device', hidden: true }))
    await vi.waitFor(() => expect(beginOverride).toHaveBeenCalledOnce())

    fireEvent.click(chip)
    fireEvent.click(screen.getByRole('menuitemradio', { name: 'Use account default', hidden: true }))
    await vi.waitFor(() => expect(clearOverride).toHaveBeenCalledOnce())
  })

  it('dual rows show the override state on the chip', () => {
    render(() => (
      <SettingRow
        descriptor={descriptor({ scope: 'dual' })}
        binding={binding({ overridden: () => true })}
      />
    ))
    const chip = screen.getByTestId('scope-chip-test.row')
    expect(chip.textContent).toContain('This device')
    expect(chip.querySelector('svg')).toBeTruthy()
  })

  it('single-tier rows render the plain tier note instead of a menu', () => {
    render(() => <SettingRow descriptor={descriptor({ scope: 'browser' })} binding={binding()} />)
    expect(screen.getByText('This device')).toBeTruthy()
    expect(screen.queryByRole('button', { name: /This device/ })).toBeNull()

    cleanup()
    render(() => <SettingRow descriptor={descriptor({ scope: 'account' })} binding={binding()} />)
    expect(screen.getByText('Account')).toBeTruthy()

    cleanup()
    render(() => <SettingRow descriptor={descriptor({ scope: 'hub' })} binding={binding()} />)
    expect(screen.getByText('Hub')).toBeTruthy()
  })
})

describe('settingRow write sequencing', () => {
  // A superseded rejection must not state itself. The store and the account
  // path both already skip their own bookkeeping for one, but both still
  // reject — so without a row-level guard the row shows the value the LATER
  // write stored beside the reason the EARLIER one failed, permanently.
  it('does not show a superseded write\'s error beside a later success', async () => {
    let rejectFirst: (err: Error) => void = () => {}
    const set = vi.fn()
      .mockImplementationOnce(() => new Promise((_resolve, reject) => {
        rejectFirst = reject
      }))
      .mockImplementationOnce(async () => {})

    render(() => (
      <SettingRow
        descriptor={descriptor({ control: { kind: 'toggle' } })}
        binding={binding({ value: () => false, set })}
      />
    ))
    const toggle = screen.getByRole('switch', { name: 'Test row' })

    fireEvent.click(toggle) // write 1, still in flight
    fireEvent.click(toggle) // write 2, supersedes it
    await Promise.resolve()

    rejectFirst(new Error('the hub refused write one'))
    await new Promise(resolve => setTimeout(resolve, 0))

    expect(screen.queryByText('the hub refused write one')).toBeNull()
  })

  /**
   * The typed text lives in the DOM until the binding accepts it, and
   * `props.value` does not change when the binding REFUSES. Solid assigns
   * `value` only when the tracked expression changes, so the field kept
   * showing a string the hub never stored, for the life of the dialog.
   */
  it('puts the stored text back when the write is refused', async () => {
    const set = vi.fn(async () => {
      throw new Error('public base URL must be absolute')
    })
    render(() => (
      <SettingRow
        descriptor={descriptor({ control: { kind: 'text' } })}
        binding={binding({ value: () => 'https://hub.example.com', set })}
      />
    ))
    const input = screen.getByRole('textbox', { name: 'Test row' }) as HTMLInputElement
    fireEvent.change(input, { target: { value: 'not-a-url' } })

    await waitFor(() => expect(screen.getByText('public base URL must be absolute')).toBeTruthy())
    expect(input.value).toBe('https://hub.example.com')
  })

  it('keeps the accepted text', async () => {
    const set = vi.fn(async () => {})
    render(() => (
      <SettingRow
        descriptor={descriptor({ control: { kind: 'text' } })}
        binding={binding({ value: () => 'https://hub.example.com', set })}
      />
    ))
    const input = screen.getByRole('textbox', { name: 'Test row' }) as HTMLInputElement
    fireEvent.change(input, { target: { value: 'https://other.example.com' } })
    await waitFor(() => expect(set).toHaveBeenCalledWith('https://other.example.com'))
    expect(input.value).toBe('https://other.example.com')
  })

  // The pill branch re-derives every pill from `props.value`, so only the
  // wide-enum `<select>` needs the repair.
  it('puts the stored option back when a select write is refused', async () => {
    const set = vi.fn(async () => {
      throw new Error('unknown TLS mode')
    })
    render(() => (
      <SettingRow
        descriptor={descriptor({ control: { kind: 'enum', options: [
          { value: 'none', label: 'None' },
          { value: 'starttls', label: 'STARTTLS' },
          { value: 'tls', label: 'TLS' },
          { value: 'auto', label: 'Auto' },
          { value: 'legacy', label: 'Legacy' },
        ] } })}
        binding={binding({ value: () => 'starttls', set })}
      />
    ))
    const select = screen.getByRole('combobox', { name: 'Test row' }) as HTMLSelectElement
    fireEvent.change(select, { target: { value: 'legacy' } })

    await waitFor(() => expect(screen.getByText('unknown TLS mode')).toBeTruthy())
    expect(select.value).toBe('starttls')
  })

  // The set path and the reset path share ONE counter, because they write
  // the same row: a Reset issued after a set supersedes it. Two counters
  // (or one per operation kind) would let the set's rejection state itself
  // beside the value the reset restored.
  it('does not show a superseded write\'s error after a later reset', async () => {
    let rejectSet: (err: Error) => void = () => {}
    let releaseReset: () => void = () => {}
    const set = vi.fn(() => new Promise<void>((_resolve, reject) => {
      rejectSet = reject
    }))
    const reset = vi.fn(() => new Promise<void>((resolve) => {
      releaseReset = resolve
    }))

    render(() => (
      <SettingRow
        descriptor={descriptor({ control: { kind: 'toggle' } })}
        binding={binding({ value: () => false, set, customized: () => true, reset })}
      />
    ))

    fireEvent.click(screen.getByRole('switch', { name: 'Test row' })) // write 1
    await Promise.resolve()
    fireEvent.click(screen.getByTestId('setting-reset-test.row')) // supersedes it
    await Promise.resolve()
    expect(reset).toHaveBeenCalled()

    rejectSet(new Error('the hub refused write one'))
    await new Promise(resolve => setTimeout(resolve, 0))

    // The reset has NOT replied yet, so nothing could have cleared a
    // recorded error: the shared counter is the only reason there is none.
    expect(screen.queryByText('the hub refused write one')).toBeNull()

    releaseReset()
    await new Promise(resolve => setTimeout(resolve, 0))
  })

  it('still shows the newest write\'s error', async () => {
    const set = vi.fn(async () => {
      throw new Error('the hub refused this one')
    })
    render(() => (
      <SettingRow
        descriptor={descriptor({ control: { kind: 'toggle' } })}
        binding={binding({ value: () => false, set })}
      />
    ))
    fireEvent.click(screen.getByRole('switch', { name: 'Test row' }))
    await waitFor(() => expect(screen.getByText('the hub refused this one')).toBeTruthy())
  })
})

describe('formatEffectiveValue', () => {
  it('reads a toggle as On and Off, never as a JSON literal', () => {
    expect(formatEffectiveValue({ kind: 'toggle' }, true)).toBe('On')
    expect(formatEffectiveValue({ kind: 'toggle' }, false)).toBe('Off')
  })

  // ToggleControl renders `value() === true`, so a value that is not the
  // boolean true reads OFF in the control. The note has to agree with it.
  it.each([
    ['the string "true"', 'true'],
    ['the number 1', 1],
    ['null', null],
  ])('reads %s as Off, the state its switch shows', (_label, value) => {
    expect(formatEffectiveValue({ kind: 'toggle' }, value)).toBe('Off')
  })

  it('reads an enum by the label its options carry', () => {
    const control = { kind: 'enum' as const, options: [
      { value: 'altcha', label: 'ALTCHA' },
      { value: 'recaptcha_v3', label: 'Google reCAPTCHA v3' },
    ] }
    expect(formatEffectiveValue(control, 'recaptcha_v3')).toBe('Google reCAPTCHA v3')
  })

  // A newer hub can enforce a value this client has no option for. The raw
  // text is what is left to report, and an empty note reports nothing.
  it('keeps the raw text of an enum value that matches no option', () => {
    expect(formatEffectiveValue({ kind: 'enum', options: [{ value: 'altcha', label: 'ALTCHA' }] }, 'hcaptcha'))
      .toBe('hcaptcha')
  })

  // NumberControl renders the unit as a separate label in a flex row with
  // a gap, so the note spaces it the same way.
  it('spaces a number from its unit, as its control spaces them', () => {
    expect(formatEffectiveValue({ kind: 'number', unit: 'seconds' }, 604800)).toBe('604800 seconds')
    expect(formatEffectiveValue({ kind: 'number', min: 0, unit: 'bytes' }, 268435456)).toBe('268435456 bytes')
  })

  // SliderControl concatenates the unit onto its readout (`40%`), and `%`
  // is the only unit a slider carries.
  it('attaches a slider unit to the figure, as its readout does', () => {
    expect(formatEffectiveValue({ kind: 'slider', min: 0, max: 100, step: 1, unit: '%' }, 40)).toBe('40%')
  })

  // The unit guard tests the UNIT, never the value. A queue budget of 0
  // auto-sizes, so 0 is a real enforced figure and keeps its unit.
  it('keeps the unit on a falsy 0', () => {
    expect(formatEffectiveValue({ kind: 'number', min: 0, unit: 'bytes' }, 0)).toBe('0 bytes')
    expect(formatEffectiveValue({ kind: 'slider', min: 0, max: 100, step: 1, unit: '%' }, 0)).toBe('0%')
  })

  // Most number keys declare no unit at all. A missing one must leave no
  // stray separator and no `undefined`.
  it('adds no separator to a number that declares no unit', () => {
    expect(formatEffectiveValue({ kind: 'number', min: 0 }, 0)).toBe('0')
    expect(formatEffectiveValue({ kind: 'number' }, 42)).toBe('42')
    expect(formatEffectiveValue({ kind: 'slider', min: 0, max: 100, step: 1 }, 40)).toBe('40')
  })

  // A string and a list read the same in the note as in the control, so
  // the value stands as it is.
  it('prints a string and a list as they are', () => {
    expect(formatEffectiveValue({ kind: 'text' }, 'mail.example.com')).toBe('mail.example.com')
    expect(formatEffectiveValue({ kind: 'stringList', addLabel: 'Add' }, ['Inter', 'Hack'])).toBe('Inter,Hack')
  })
})
