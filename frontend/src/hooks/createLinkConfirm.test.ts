import type { LinkConfirmState } from './createLinkConfirm'
import { describe, expect, it } from 'vitest'
import { createDialogState } from './createDialogState'
import { createLinkConfirm } from './createLinkConfirm'

describe('createLinkConfirm', () => {
  it('opens the prompt with the request, and resolves what the user chose', async () => {
    const dialog = createDialogState<LinkConfirmState>()
    const confirmLink = createLinkConfirm(dialog)
    const request = {
      uri: 'https://evil.example/',
      label: 'https://good.example/',
      insecure: false,
      labelMismatch: true,
      misleadingLabel: 'https://good.example/',
    }

    const answer = confirmLink(request)
    expect(dialog.value()?.request).toEqual(request)

    dialog.value()?.resolve(true)
    await expect(answer).resolves.toBe(true)
  })

  it('refuses a still-open prompt before it asks the next one', async () => {
    const dialog = createDialogState<LinkConfirmState>()
    const confirmLink = createLinkConfirm(dialog)
    const base = { insecure: false, labelMismatch: true, misleadingLabel: null }

    // One slot holds one request, so the abandoned one must be ANSWERED. A
    // dropped resolver would park the first click's promise forever.
    const first = confirmLink({ uri: 'https://one.test/', label: 'one', ...base })
    const second = confirmLink({ uri: 'https://two.test/', label: 'two', ...base })

    await expect(first).resolves.toBe(false)
    expect(dialog.value()?.request.uri).toBe('https://two.test/')

    dialog.value()?.resolve(true)
    await expect(second).resolves.toBe(true)
  })
})
