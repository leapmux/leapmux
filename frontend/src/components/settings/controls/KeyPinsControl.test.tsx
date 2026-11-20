import type { KeyPinEntry } from '~/lib/keyPinStore'
import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { accountStorageKey, KEY_KEY_PINS, readKeyPinState, setStorageAccountForTests, storageAccountId } from '~/lib/browserStorage'
import { writeTrustRowForTests } from '~/lib/browserStorageDb'
import { deferred, settle } from '~/test-support/async'
import { seedKeyPin, testKeyPinHex } from '~/test-support/keyPins'
import { useTestStorage } from '~/test-support/persistentStorage'
import { KeyPinsControl } from './KeyPinsControl'

const authState = vi.hoisted(() => ({
  setUserId: undefined as undefined | ((id: string | undefined) => void),
}))
vi.mock('~/context/AuthContext', async () => {
  const { createTestWorkerTrustState } = await import('~/test-support/workerTrust')
  const state = createTestWorkerTrustState()
  authState.setUserId = state.setUserId
  return {
    useAuth: () => ({ workerTrustScope: state.scope }),
  }
})

vi.mock('~/api/clients', () => ({
  userClient: {},
  authClient: {},
}))

// Failure injection for the read and the removals. The rest of the module is
// real, so a passing test exercises the same gateway the app does.
const pinIo = vi.hoisted(() => ({
  failList: false,
  failRemove: false,
  nextList: undefined as Promise<KeyPinEntry[]> | undefined,
  nextRemove: undefined as Promise<void> | undefined,
}))
vi.mock('~/lib/keyPinStore', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/lib/keyPinStore')>()
  return {
    ...actual,
    listKeyPins: async () => {
      const next = pinIo.nextList
      pinIo.nextList = undefined
      if (next)
        return await next
      if (pinIo.failList)
        throw new Error('read refused')
      return await actual.listKeyPins()
    },
    clearKeyPin: async (workerId: string) => {
      const next = pinIo.nextRemove
      pinIo.nextRemove = undefined
      if (next) {
        await next
        return
      }
      if (pinIo.failRemove)
        throw new Error('write refused')
      await actual.clearKeyPin(workerId)
    },
    clearAllKeyPins: async () => {
      const next = pinIo.nextRemove
      pinIo.nextRemove = undefined
      if (next) {
        await next
        return
      }
      if (pinIo.failRemove)
        throw new Error('write refused')
      await actual.clearAllKeyPins()
    },
  }
})

useTestStorage()

beforeEach(() => {
  authState.setUserId?.(storageAccountId() ?? undefined)
  pinIo.failList = false
  pinIo.failRemove = false
  pinIo.nextList = undefined
  pinIo.nextRemove = undefined
})

afterEach(() => {
  cleanup()
})

async function seedPins() {
  await seedKeyPin('worker-a', {
    publicKeyHex: testKeyPinHex('aa'),
    firstSeen: 1000,
  })
  await seedKeyPin('worker-b', {
    publicKeyHex: testKeyPinHex('bb'),
    firstSeen: 2000,
  })
}

function shownPinRows(): string[] {
  return screen.queryAllByTestId(/^key-pin-remove-/)
    .map(b => (b.dataset.testid ?? '').replace('key-pin-remove-', ''))
}

/** Wait for the view to settle on exactly these rows. */
async function waitForPinRows(workerIds: string[]): Promise<void> {
  await vi.waitFor(() => expect(shownPinRows()).toEqual(workerIds))
}

describe('KeyPinsControl', () => {
  it('lists pinned workers oldest first and removes one pin after confirm', async () => {
    await seedPins()
    render(() => <KeyPinsControl />)
    await waitForPinRows(['worker-a', 'worker-b'])

    const removeA = screen.getByTestId('key-pin-remove-worker-a')
    fireEvent.click(removeA)
    expect(removeA).toHaveTextContent('Confirm?')
    expect(removeA.className).toMatch(/small/)

    fireEvent.click(removeA)
    await waitForPinRows(['worker-b'])
  })

  it('lets the armed Remove reach a screen reader', async () => {
    await seedPins()
    render(() => <KeyPinsControl />)
    await waitForPinRows(['worker-a', 'worker-b'])
    const remove = screen.getByTestId('key-pin-remove-worker-a')
    // No `aria-label`, so the NAME is the button's own text -- and the text is
    // what `ConfirmButton` swaps when it arms. A `<Tooltip ariaLabel>` around
    // this button pinned the name to "Remove" in both states, so a screen
    // reader heard "Remove" for the confirming click too and never learnt that
    // the first click had armed anything.
    expect(remove).not.toHaveAttribute('aria-label')
    expect(remove).toHaveAccessibleName('Remove')

    fireEvent.click(remove)
    expect(remove).toHaveAccessibleName('Confirm?')
  })

  it('requires confirmation before discarding a corrupt account pin map', async () => {
    await writeTrustRowForTests({
      k: accountStorageKey(storageAccountId()!, KEY_KEY_PINS),
      v: 'corrupt',
      e: Date.now() + 60_000,
    })
    render(() => <KeyPinsControl />)
    const discard = await screen.findByTestId('key-pins-discard-corrupt')
    expect(discard).toHaveTextContent('Discard corrupt pin map')
    fireEvent.click(discard)
    expect(discard).toHaveTextContent('Confirm?')
    fireEvent.click(discard)
    await vi.waitFor(async () => expect(await readKeyPinState()).toEqual({ pins: {}, removalRevision: 1 }))
  })

  it('puts the worker id above the date and Remove actions', async () => {
    await seedPins()
    render(() => <KeyPinsControl />)
    await waitForPinRows(['worker-a', 'worker-b'])
    const row = screen.getByTestId('key-pin-id-worker-a').closest('[data-worker]')!
    expect(row.querySelector('[data-testid="key-pin-id-worker-a"]')).toBeTruthy()
    expect(row.querySelector('[data-testid="key-pin-remove-worker-a"]')).toBeTruthy()
    // Id precedes the meta row (date + Remove) in DOM order so the stacked
    // phone layout and the desktop row both read id first.
    const id = screen.getByTestId('key-pin-id-worker-a')
    const remove = screen.getByTestId('key-pin-remove-worker-a')
    expect(id.compareDocumentPosition(remove) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0)
  })

  it('removes every pin with Remove all after confirm', async () => {
    await seedPins()
    render(() => <KeyPinsControl />)
    await waitForPinRows(['worker-a', 'worker-b'])
    const removeAll = screen.getByTestId('key-pins-remove-all')
    expect(removeAll).toHaveAttribute('data-variant', 'danger')
    expect(screen.getByTestId('key-pin-remove-worker-a')).not.toHaveAttribute('data-variant')
    fireEvent.click(removeAll)
    expect(removeAll).toHaveTextContent('Confirm?')

    fireEvent.click(removeAll)
    await vi.waitFor(() => expect(screen.getByText(/No worker keys pinned/)).toBeTruthy())
    expect(screen.queryByTestId('key-pins-remove-all')).toBeNull()
  })

  it('renders the empty state only after a read confirms that nothing is pinned', async () => {
    render(() => <KeyPinsControl />)
    expect(screen.queryByText(/No worker keys pinned/)).toBeNull()
    expect(await screen.findByText(/No worker keys pinned/)).toBeTruthy()
    expect(screen.queryByTestId('key-pins-remove-all')).toBeNull()
  })

  // The plan's "awaited failure and retry": a read that rejects is shown with
  // what to do next, and a retry that now succeeds replaces it with the list.
  it('shows a read failure with Retry and clears it when the retry succeeds', async () => {
    await seedPins()
    render(() => <KeyPinsControl />)
    await waitForPinRows(['worker-a', 'worker-b'])

    // A committed change elsewhere triggers the re-read that fails.
    pinIo.failList = true
    await seedKeyPin('worker-c', {
      publicKeyHex: testKeyPinHex('cc'),
      firstSeen: 3000,
    })
    await vi.waitFor(() => {
      expect(screen.getByTestId('key-pins-read-error')).toHaveTextContent(/read refused/)
      expect(screen.getByTestId('key-pins-retry')).toBeTruthy()
    })

    pinIo.failList = false
    fireEvent.click(screen.getByTestId('key-pins-retry'))
    await waitForPinRows(['worker-a', 'worker-b', 'worker-c'])
    expect(screen.queryByTestId('key-pins-read-error')).toBeNull()
  })

  it('shows a first read failure without claiming that no keys are pinned', async () => {
    await seedPins()
    pinIo.failList = true
    render(() => <KeyPinsControl />)
    await screen.findByTestId('key-pins-read-error')
    expect(screen.queryByText(/No worker keys pinned/)).toBeNull()

    pinIo.failList = false
    fireEvent.click(screen.getByTestId('key-pins-retry'))
    await waitForPinRows(['worker-a', 'worker-b'])
    expect(screen.queryByTestId('key-pins-read-error')).toBeNull()
  })

  it.each(['success', 'failure'])('discards an old read %s after switching away and back', async (outcome) => {
    await seedPins()
    const account = storageAccountId()!
    const original = deferred<KeyPinEntry[]>()
    pinIo.nextList = original.promise
    render(() => <KeyPinsControl />)
    expect(pinIo.nextList).toBeUndefined()

    setStorageAccountForTests('another-account')
    setStorageAccountForTests(account)
    await waitForPinRows(['worker-a', 'worker-b'])

    if (outcome === 'success') {
      original.resolve([
        {
          workerId: 'stale-worker',
          publicKeyHex: testKeyPinHex('cc'),
          firstSeen: 1000,
        },
      ])
    }
    else {
      original.reject(new Error('stale read error'))
    }
    await settle()

    expect(shownPinRows()).toEqual(['worker-a', 'worker-b'])
    expect(screen.queryByTestId('key-pins-read-error')).toBeNull()
  })

  it('retains the last known list on a same-account read failure', async () => {
    await seedPins()
    render(() => <KeyPinsControl />)
    await waitForPinRows(['worker-a', 'worker-b'])

    pinIo.failList = true
    await seedKeyPin('worker-c', {
      publicKeyHex: testKeyPinHex('cc'),
      firstSeen: 3000,
    })
    // The failure is of the storage, not of the data: the rows already on
    // screen belong to this account and stay, beside the reason.
    await vi.waitFor(() => expect(screen.getByTestId('key-pins-read-error')).toBeTruthy())
    expect(screen.getByTestId('key-pin-id-worker-a')).toBeTruthy()
    expect(screen.getByTestId('key-pin-id-worker-b')).toBeTruthy()
  })

  it('drops the last known list when the account changes', async () => {
    await seedPins()
    render(() => <KeyPinsControl />)
    await waitForPinRows(['worker-a', 'worker-b'])

    // The pins belong to an account this document no longer holds.
    setStorageAccountForTests('another-account')
    await vi.waitFor(() => expect(screen.getByText(/No worker keys pinned/)).toBeTruthy())
    expect(screen.queryByTestId('key-pin-id-worker-a')).toBeNull()
  })

  it.each(['key-pin-remove-worker-a', 'key-pins-remove-all'])('discards a failed removal from the previous account (%s)', async (testId) => {
    await seedPins()
    render(() => <KeyPinsControl />)
    await waitForPinRows(['worker-a', 'worker-b'])

    const oldRemoval = deferred<void>()
    pinIo.nextRemove = oldRemoval.promise
    fireEvent.click(screen.getByTestId(testId))
    fireEvent.click(screen.getByTestId(testId))
    expect(pinIo.nextRemove).toBeUndefined()

    setStorageAccountForTests('another-account')
    await seedKeyPin('successor-worker', {
      publicKeyHex: testKeyPinHex('cc'),
      firstSeen: 1000,
    })
    await waitForPinRows(['successor-worker'])
    const successorButton = screen.getByTestId('key-pin-remove-successor-worker')
    const successorRemoval = deferred<void>()
    pinIo.nextRemove = successorRemoval.promise
    fireEvent.click(successorButton)
    fireEvent.click(successorButton)
    const successorStarted = pinIo.nextRemove === undefined

    oldRemoval.reject(new Error('old account write refused'))
    await settle()
    const successorStillBusy = successorButton.hasAttribute('disabled')
    const oldError = screen.queryByTestId('key-pins-remove-error')
    successorRemoval.resolve()
    await settle()

    expect(successorStarted).toBe(true)
    expect(successorStillBusy).toBe(true)
    expect(oldError).toBeNull()
    expect(successorButton).toBeEnabled()
  })

  it('shows a removal failure and keeps the row', async () => {
    await seedPins()
    render(() => <KeyPinsControl />)
    await waitForPinRows(['worker-a', 'worker-b'])

    pinIo.failRemove = true
    fireEvent.click(screen.getByTestId('key-pin-remove-worker-a'))
    fireEvent.click(screen.getByTestId('key-pin-remove-worker-a'))
    // The removal failed, so the pin is still pinned: the row stays on screen
    // beside the reason rather than vanishing as if it had been removed.
    await vi.waitFor(() => expect(screen.getByTestId('key-pins-remove-error')).toHaveTextContent(/write refused/))
    expect(screen.getByTestId('key-pin-id-worker-a')).toBeTruthy()
    expect(screen.getByTestId('key-pin-id-worker-b')).toBeTruthy()
  })
})
