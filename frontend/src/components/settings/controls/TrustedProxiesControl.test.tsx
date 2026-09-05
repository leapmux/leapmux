import type { SettingBinding } from '../types'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { MAX_TRUSTED_PROXY_SELECTORS, TRUSTED_PROXY_PROVIDERS } from '~/generated/contracts/trusted-proxies'
import { TrustedProxiesControl } from './TrustedProxiesControl'

function binding(initial: string[], write = vi.fn()) {
  const [value, setValue] = createSignal<unknown>(initial)
  return {
    model: {
      value,
      set: async (next: unknown) => {
        await write(next)
        setValue(next)
      },
    } satisfies SettingBinding,
    setValue,
    write,
  }
}

function openAddMenu() {
  fireEvent.click(screen.getByRole('button', { name: /^Add/ }))
}

/** The manual entry is a plain action; each provider is a checkable state. */
function chooseManualAdd() {
  openAddMenu()
  fireEvent.click(screen.getByRole('menuitem', { name: /IP address or range/, hidden: true }))
}

function chooseProvider(label: string) {
  openAddMenu()
  fireEvent.click(screen.getByRole('menuitemcheckbox', { name: new RegExp(label), hidden: true }))
}

describe('trustedProxiesControl', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  // The address forms are LITERAL here on purpose: they are what the parser
  // accepts, and restating them is how this test would catch a form dropped
  // from the list. The provider tokens are DERIVED, because they and the Add
  // menu are two renderings of one generated catalogue -- listing them twice
  // is how a new contract entry reaches the menu and misses the examples.
  it('shows every supported manual example and provider token', () => {
    render(() => <TrustedProxiesControl binding={binding([]).model} />)
    expect(TRUSTED_PROXY_PROVIDERS.length).toBeGreaterThan(0)
    for (const example of [
      '192.0.2.10',
      '2001:db8::10',
      '192.168.0.1-100',
      '192.168.0.1-192.168.0.100',
      '2001:db8::1-2001:db8::ffff',
      '192.168.0.0/24',
      '2001:db8::/64',
      ...TRUSTED_PROXY_PROVIDERS.map(provider => provider.token),
    ]) {
      expect(screen.getByText(example)).toBeInTheDocument()
    }
  })

  it('shows the non-dismissible provider security warning', () => {
    render(() => <TrustedProxiesControl binding={binding([]).model} />)
    const warning = screen.getByRole('alert')
    expect(warning).toHaveTextContent('shared provider infrastructure, not your account')
    expect(warning).toHaveTextContent('authenticated origin requests')
    expect(warning).toHaveTextContent('remove client-supplied forwarding headers')
    expect(warning.querySelector('button')).toBeNull()
  })

  it('adds and edits a manual selector before apply', () => {
    const current = binding([])
    render(() => <TrustedProxiesControl binding={current.model} />)
    chooseManualAdd()

    const input = screen.getByLabelText('Trusted proxy selector')
    fireEvent.input(input, { target: { value: '192.0.2.10' } })
    fireEvent.click(screen.getByRole('button', { name: 'Apply' }))

    expect(current.write).toHaveBeenCalledWith(['192.0.2.10'])
  })

  // The manual entry is an ACTION, so choosing it twice stages two rows. A
  // checkable item could not express that: the second choice would read as
  // unchecking the first.
  it('stages one row for each manual add', () => {
    const current = binding([])
    render(() => <TrustedProxiesControl binding={current.model} />)
    chooseManualAdd()
    chooseManualAdd()

    const inputs = screen.getAllByLabelText('Trusted proxy selector')
    expect(inputs).toHaveLength(2)
    fireEvent.input(inputs[0], { target: { value: '192.0.2.10' } })
    fireEvent.input(inputs[1], { target: { value: '2001:db8::/64' } })
    fireEvent.click(screen.getByRole('button', { name: 'Apply' }))

    expect(current.write).toHaveBeenCalledWith(['192.0.2.10', '2001:db8::/64'])
  })

  it('adds providers symbolically and disables a duplicate provider', () => {
    const current = binding([])
    render(() => <TrustedProxiesControl binding={current.model} />)
    chooseProvider('Cloudflare')

    expect(screen.getByDisplayValue('cloudflare')).toBeInTheDocument()
    openAddMenu()
    expect(screen.getByRole('menuitemcheckbox', { name: /Cloudflare/, hidden: true })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Apply' }))
    expect(current.write).toHaveBeenCalledWith(['cloudflare'])
  })

  it('preserves provider tokens after read-back', () => {
    render(() => <TrustedProxiesControl binding={binding(['cloudfront']).model} />)
    expect(screen.getByDisplayValue('cloudfront')).toBeInTheDocument()
    expect(screen.queryByDisplayValue(/\/\d+$/)).toBeNull()
  })

  it('removes staged rows and restores them on cancel', () => {
    render(() => <TrustedProxiesControl binding={binding(['cloudflare', '192.0.2.1']).model} />)
    fireEvent.click(screen.getByRole('button', { name: 'Remove cloudflare' }))
    expect(screen.queryByDisplayValue('cloudflare')).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.getByDisplayValue('cloudflare')).toBeInTheDocument()
    expect(screen.getByDisplayValue('192.0.2.1')).toBeInTheDocument()
  })

  it('follows an external reset when no edit is staged', () => {
    const current = binding(['cloudflare'])
    render(() => <TrustedProxiesControl binding={current.model} />)
    current.setValue([])
    expect(screen.queryByDisplayValue('cloudflare')).toBeNull()
    expect(screen.getByText(/No proxy is trusted/)).toBeInTheDocument()
  })

  it('keeps staged rows and shows a backend refusal', async () => {
    const current = binding(['192.0.2.1'], vi.fn().mockRejectedValue(new Error('selector overlaps an earlier selector')))
    render(() => <TrustedProxiesControl binding={current.model} />)
    fireEvent.input(screen.getByLabelText('Trusted proxy selector'), { target: { value: '192.0.2.0/24' } })
    fireEvent.click(screen.getByRole('button', { name: 'Apply' }))

    expect(await screen.findByText(/overlaps an earlier selector/)).toBeInTheDocument()
    expect(screen.getByDisplayValue('192.0.2.0/24')).toBeInTheDocument()
  })

  it('stops adding selectors at the backend cap', () => {
    const selectors = Array.from({ length: MAX_TRUSTED_PROXY_SELECTORS }, (_, index) => `192.0.2.${index + 1}`)
    render(() => <TrustedProxiesControl binding={binding(selectors).model} />)
    expect(screen.getByRole('button', { name: /^Add/ })).toBeDisabled()
    expect(screen.getByText(`At most ${MAX_TRUSTED_PROXY_SELECTORS} selectors.`)).toBeInTheDocument()
  })
})
