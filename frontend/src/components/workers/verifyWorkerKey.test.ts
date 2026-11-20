import { describe, expect, it, vi } from 'vitest'
import { verifyWorkerKey } from './verifyWorkerKey'

const channelState = vi.hoisted(() => ({
  getOrOpenChannel: vi.fn<(workerId: string) => Promise<string>>(),
}))
vi.mock('~/api/workerRpc', () => ({
  channelManager: {
    getOrOpenChannel: (workerId: string) => channelState.getOrOpenChannel(workerId),
  },
}))

// Declared hoisted: the mock factories run before the module body, so a class
// declared alongside the tests is not initialized when one of them reads it.
const KeyPinRejectedError = vi.hoisted(() => class KeyPinRejectedError extends Error {})
vi.mock('~/lib/keyPinStore', () => ({
  KeyPinRejectedError,
}))

describe('verifyWorkerKey', () => {
  it('opens the browser channel to the worker', async () => {
    channelState.getOrOpenChannel.mockResolvedValueOnce('ch-1')
    await verifyWorkerKey('w1')
    expect(channelState.getOrOpenChannel).toHaveBeenCalledWith('w1')
  })

  // A rejection is the user's own decision in the key-changed dialog. The
  // message must say so, or the caller reads it as a transport failure and
  // offers to retry the same thing.
  it('reports a rejected key change as a decision to confirm, not a failure', async () => {
    channelState.getOrOpenChannel.mockRejectedValue(new KeyPinRejectedError())

    await expect(verifyWorkerKey('w1')).rejects.toThrow(/was rejected/)
    await expect(verifyWorkerKey('w1')).rejects.toThrow(/fingerprint/)
    channelState.getOrOpenChannel.mockReset()
  })

  it('reports any other failure with its reason', async () => {
    channelState.getOrOpenChannel.mockRejectedValueOnce(new Error('hub unreachable'))

    await expect(verifyWorkerKey('w1')).rejects.toThrow('Could not verify the Worker key: hub unreachable')
  })

  it('falls back to its own wording for a rejection that is not an Error', async () => {
    channelState.getOrOpenChannel.mockRejectedValueOnce('nope')

    await expect(verifyWorkerKey('w1')).rejects.toThrow('Could not verify the Worker key: the channel did not open')
  })
})
