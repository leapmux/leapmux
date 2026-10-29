import { BroadcastChannel as NativeBroadcastChannel } from 'node:worker_threads'

/** Keep each test file's native broadcast bus with its independent database. */
export function installBroadcastChannelIsolation(testFilePath: string | undefined): void {
  if (testFilePath === undefined || testFilePath.length === 0)
    throw new Error('Test broadcasts require a nonempty test file path.')

  class TestFileBroadcastChannel extends NativeBroadcastChannel {
    constructor(name: string) {
      super(JSON.stringify([testFilePath, name]))
      // The public name stays logical, as it does for channels on two origins.
      Object.defineProperty(this, 'name', {
        configurable: true,
        get: () => name,
      })
    }
  }

  // This is the file's baseline. Test stubs restore it through vi.unstubAllGlobals.
  Object.defineProperty(globalThis, 'BroadcastChannel', {
    configurable: true,
    writable: true,
    value: TestFileBroadcastChannel,
  })
}
