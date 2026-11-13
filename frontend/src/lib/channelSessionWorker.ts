import type { SessionCommand } from './channelSessionProtocol'
import { createChannelSessionEndpoint } from './channelSessionEndpoint'

// ---------------------------------------------------------------------------
// Channel session worker
//
// Hosts the Noise session of every channel of the tab (see
// channelSessionEndpoint.ts), so that the handshake, the SLH-DSA verification,
// encryption, decryption and the in-band rekey run off the main thread, and the
// main thread never holds a cipher key. One worker serves every channel of the
// tab, and lives until the main thread shuts it down or the page goes away.
//
// This module must import neither channelSessionClient.ts nor channel.ts, or
// the worker bundle would hold a `new Worker(...)` of its own and the main
// thread's RPC layer.
// ---------------------------------------------------------------------------

const endpoint = createChannelSessionEndpoint({
  post: (event, transfer) => globalThis.postMessage(event, { transfer }),
  closeSelf: () => globalThis.close(),
})

globalThis.onmessage = (event: MessageEvent<SessionCommand>) => {
  endpoint.handleCommand(event.data)
}
