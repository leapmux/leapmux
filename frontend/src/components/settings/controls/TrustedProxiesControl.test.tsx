import type { SettingBinding } from '../types'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { MAX_TRUSTED_PROXY_SELECTORS } from '~/generated/contracts/trusted-proxies'
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

function chooseAddOption(label: string) {
  openAddMenu()
  fireEvent.click(screen.getByRole('menuitemcheckbox', { name: new RegExp(label), hidden: true }))
}

describe('trustedProxiesControl', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('shows every supported manual example and provider token', () => {
    render(() => <TrustedProxiesControl binding={binding([]).model} />)
    for (const example of [
      '192.0.2.10',
      '2001:db8::10',
      '192.168.0.1-100',
      '192.168.0.1-192.168.0.100',
      '2001:db8::1-2001:db8::ffff',
      '192.168.0.0/24',
      '2001:db8::/64',
      'cloudflare',
      'cloudfront',
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
    chooseAddOption('IP address or range')

    const input = screen.getByLabelText('Trusted proxy selector')
    fireEvent.input(input, { target: { value: '192.0.2.10' } })
    fireEvent.click(screen.getByRole('button', { name: 'Apply' }))

    expect(current.write).toHaveBeenCalledWith(['192.0.2.10'])
  })

  it('adds providers symbolically and disables a duplicate provider', () => {
    const current = binding([])
    render(() => <TrustedProxiesControl binding={current.model} />)
    chooseAddOption('Cloudflare')

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
