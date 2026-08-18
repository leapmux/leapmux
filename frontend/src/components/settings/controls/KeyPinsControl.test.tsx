import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { KEY_KEY_PINS, localStorageClearForTests, localStorageSet } from '~/lib/browserStorage'
import { listKeyPins } from '~/lib/keyPinStore'
import { KeyPinsControl } from './KeyPinsControl'

vi.mock('~/api/clients', () => ({
  userClient: {},
  authClient: {},
}))

function seedPins() {
  localStorageSet(KEY_KEY_PINS, {
    'worker-a': { publicKeyHex: 'aa', firstSeen: 1000 },
    'worker-b': { publicKeyHex: 'bb', firstSeen: 2000 },
  })
}

beforeEach(() => {
  localStorageClearForTests()
})

afterEach(() => {
  cleanup()
  localStorageClearForTests()
})

describe('keyPinsControl', () => {
  it('lists pinned workers oldest first and removes one pin after confirm', () => {
    seedPins()
    render(() => <KeyPinsControl />)
    const buttons = screen.getAllByTestId(/^key-pin-remove-/)
    expect(buttons.map(b => b.dataset.testid?.replace('key-pin-remove-', ''))).toEqual(['worker-a', 'worker-b'])

    const removeA = screen.getByTestId('key-pin-remove-worker-a')
    fireEvent.click(removeA)
    expect(listKeyPins().map(p => p.workerId)).toEqual(['worker-a', 'worker-b'])
    expect(removeA).toHaveTextContent('Confirm?')
    expect(removeA.className).toMatch(/small/)

    fireEvent.click(removeA)
    expect(listKeyPins().map(p => p.workerId)).toEqual(['worker-b'])
  })

  it('puts the worker id above the date and Remove actions', () => {
    seedPins()
    render(() => <KeyPinsControl />)
    const row = screen.getByTestId('key-pin-id-worker-a').closest('[data-worker]')!
    expect(row.querySelector('[data-testid="key-pin-id-worker-a"]')).toBeTruthy()
    expect(row.querySelector('[data-testid="key-pin-remove-worker-a"]')).toBeTruthy()
    // Id precedes the meta row (date + Remove) in DOM order so the stacked
    // phone layout and the desktop row both read id first.
    const id = screen.getByTestId('key-pin-id-worker-a')
    const remove = screen.getByTestId('key-pin-remove-worker-a')
    expect(id.compareDocumentPosition(remove) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0)
  })

  it('removes every pin with Remove all after confirm', () => {
    seedPins()
    render(() => <KeyPinsControl />)
    const removeAll = screen.getByTestId('key-pins-remove-all')
    expect(removeAll).toHaveAttribute('data-variant', 'danger')
    expect(screen.getByTestId('key-pin-remove-worker-a')).not.toHaveAttribute('data-variant')
    fireEvent.click(removeAll)
    expect(listKeyPins()).toHaveLength(2)
    expect(removeAll).toHaveTextContent('Confirm?')

    fireEvent.click(removeAll)
    expect(listKeyPins()).toEqual([])
    expect(screen.getByText(/No worker keys pinned/)).toBeTruthy()
  })

  it('renders the empty state when nothing is pinned', () => {
    render(() => <KeyPinsControl />)
    expect(screen.getByText(/No worker keys pinned/)).toBeTruthy()
    expect(screen.queryByTestId('key-pins-remove-all')).toBeNull()
  })
})
