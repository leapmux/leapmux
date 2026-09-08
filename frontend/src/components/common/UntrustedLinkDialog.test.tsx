import type { UntrustedLinkConfirmRequest } from '~/lib/untrustedLinks'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { UntrustedLinkDialog } from './UntrustedLinkDialog'

// jsdom does not implement the native <dialog> API. Keep the `open` attribute
// and the `close` event consistent, so Dialog's mount and cleanup behave as
// they do in a browser.
beforeEach(() => {
  HTMLDialogElement.prototype.showModal = vi.fn(function (this: HTMLDialogElement) {
    this.setAttribute('open', '')
  })
  HTMLDialogElement.prototype.close = vi.fn(function (this: HTMLDialogElement) {
    this.removeAttribute('open')
    this.dispatchEvent(new Event('close'))
  })
})

function requestOf(overrides: Partial<UntrustedLinkConfirmRequest> = {}): UntrustedLinkConfirmRequest {
  return {
    uri: 'https://evil.example/steal',
    label: 'Click here',
    insecure: false,
    labelMismatch: true,
    misleadingLabel: null,
    ...overrides,
  }
}

describe('terminal link dialog', () => {
  it('shows both the text and the address, so the reader can compare them', () => {
    render(() => <UntrustedLinkDialog request={requestOf()} onResolve={() => {}} />)

    expect(screen.getByTestId('untrusted-link-shown')).toHaveTextContent('Click here')
    expect(screen.getByTestId('untrusted-link-target')).toHaveTextContent('https://evil.example/steal')
  })

  it('names the deception when the text is an address of its own', () => {
    render(() => (
      <UntrustedLinkDialog
        request={requestOf({ label: 'https://good.example', misleadingLabel: 'https://good.example' })}
        onResolve={() => {}}
      />
    ))

    expect(screen.getByText('The link text looks like an address. It is not the address that this link opens.')).toBeInTheDocument()
  })

  it('states the plainer reason when the text is not an address', () => {
    render(() => <UntrustedLinkDialog request={requestOf()} onResolve={() => {}} />)

    expect(screen.getByText('The link text does not name the address that it opens.')).toBeInTheDocument()
  })

  it('warns about the missing encryption, and drops the text row when there is none', () => {
    render(() => (
      <UntrustedLinkDialog
        request={requestOf({ uri: 'http://example.test/', label: '', labelMismatch: false, insecure: true })}
        onResolve={() => {}}
      />
    ))

    expect(screen.getByText('This address is not encrypted.')).toBeInTheDocument()
    expect(screen.getByText(/travels unencrypted/)).toBeInTheDocument()
    expect(screen.queryByTestId('untrusted-link-shown')).not.toBeInTheDocument()
  })

  it('resolves true on open and false on cancel', () => {
    const onResolve = vi.fn()
    render(() => <UntrustedLinkDialog request={requestOf()} onResolve={onResolve} />)

    fireEvent.click(screen.getByTestId('untrusted-link-open'))
    expect(onResolve).toHaveBeenCalledWith(true)

    fireEvent.click(screen.getByTestId('untrusted-link-cancel'))
    expect(onResolve).toHaveBeenLastCalledWith(false)
  })

  it('leaves a prose label unarmed: it states no destination to misstate', () => {
    const onResolve = vi.fn()
    render(() => <UntrustedLinkDialog request={requestOf()} onResolve={onResolve} />)

    fireEvent.click(screen.getByTestId('untrusted-link-open'))
    expect(onResolve).toHaveBeenCalledWith(true)
  })

  it('arms the open button for a plaintext address too', () => {
    const onResolve = vi.fn()
    render(() => (
      <UntrustedLinkDialog
        request={requestOf({ uri: 'http://foo.test/bar', label: 'foo.test/bar', labelMismatch: false, insecure: true })}
        onResolve={onResolve}
      />
    ))

    fireEvent.click(screen.getByTestId('untrusted-link-open'))
    expect(onResolve).not.toHaveBeenCalled()
    fireEvent.click(screen.getByTestId('untrusted-link-open'))
    expect(onResolve).toHaveBeenCalledWith(true)
  })

  it('arms the open button for the deceptive case', () => {
    const onResolve = vi.fn()
    render(() => (
      <UntrustedLinkDialog
        request={requestOf({ label: 'https://good.example', misleadingLabel: 'https://good.example' })}
        onResolve={onResolve}
      />
    ))

    // ConfirmButton takes two clicks: the first only arms it, so a reader who
    // clicks through the prompt still stops once before a deceptive link opens.
    fireEvent.click(screen.getByTestId('untrusted-link-open'))
    expect(onResolve).not.toHaveBeenCalled()

    fireEvent.click(screen.getByTestId('untrusted-link-open'))
    expect(onResolve).toHaveBeenCalledWith(true)
  })
})
