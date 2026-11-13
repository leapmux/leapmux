import type { SessionWorkerHandlers, SessionWorkerPort } from './channelSessionClient'
import type { ChannelSessionEndpoint, ChannelSessionEndpointOptions } from './channelSessionEndpoint'
import type { SessionCommand, SessionEvent } from './channelSessionProtocol'
import { createChannelSessionEndpoint } from './channelSessionEndpoint'
import { formatErrorMessage } from './errors'

// ---------------------------------------------------------------------------
// In-process channel session host
//
// Hosts the channel session endpoint on this thread, behind a real
// MessageChannel pair, for the places that have no Worker: the Node E2E harness
// and the unit tests. Real ports keep the two properties that a Worker has and a
// direct call would hide: delivery is asynchronous and in order, and a
// transferred buffer is detached on the sender.
//
// The browser main bundle must never import this module: it would put the
// cipher keys on the main thread (workerOnlyCode.test.ts).
// ---------------------------------------------------------------------------

export interface InProcessSessionHost {
  /** Spawn one endpoint behind a fresh MessageChannel pair. Pass as ChannelManagerOpts.spawnSessionWorker. */
  spawn: () => SessionWorkerPort
  /**
   * The messages that the live spawns posted and that no handler has taken yet,
   * in both directions. A caller that waits until the host is idle polls this.
   * A command that awaits, such as an `ensureReady` that waits for a rekey,
   * counts only until its handler returns.
   */
  inFlight: () => number
  /** The endpoint of the latest spawn, or undefined before the first. */
  endpoint: () => ChannelSessionEndpoint | undefined
}

/** One spawn: its ports, and the messages in flight between them. */
interface InProcessSpawn {
  inFlight: number
  closed: boolean
}

export function createInProcessSessionHost(options: ChannelSessionEndpointOptions = {}): InProcessSessionHost {
  const live = new Set<InProcessSpawn>()
  let latest: ChannelSessionEndpoint | undefined

  function spawn(): SessionWorkerPort {
    const { port1, port2 } = new MessageChannel()
    const record: InProcessSpawn = { inFlight: 0, closed: false }
    live.add(record)
    let handlers: SessionWorkerHandlers | undefined

    /** Close both ports. A closed port delivers nothing more, so nothing stays in flight. */
    const close = (): void => {
      if (record.closed)
        return
      record.closed = true
      record.inFlight = 0
      live.delete(record)
      // An open port keeps a Node process alive.
      port1.close()
      port2.close()
    }

    /** One message was taken: count it out, unless the ports closed during its handler. */
    const taken = (): void => {
      if (!record.closed)
        record.inFlight--
    }

    const endpoint = createChannelSessionEndpoint({
      post: (event, transfer) => {
        if (record.closed)
          return
        record.inFlight++
        port2.postMessage(event, transfer)
      },
      closeSelf: close,
    }, options)
    latest = endpoint

    port2.onmessage = (event: MessageEvent<SessionCommand>) => {
      try {
        endpoint.handleCommand(event.data)
      }
      catch (err) {
        // The endpoint catches its own errors, so this is a bug in it. A Worker
        // reports an uncaught error through `error`, as this does.
        handlers?.failure(formatErrorMessage(err))
      }
      finally {
        taken()
      }
    }

    return {
      postMessage: (command, transfer) => {
        // A terminated Worker drops a message in silence too.
        if (record.closed)
          return
        record.inFlight++
        port1.postMessage(command, transfer)
      },
      connect: (connected) => {
        handlers = connected
        port1.onmessage = (event: MessageEvent<SessionEvent>) => {
          try {
            connected.message(event.data)
          }
          finally {
            taken()
          }
        }
      },
      terminate: () => {
        endpoint.dispose()
        close()
      },
    }
  }

  return {
    spawn,
    inFlight: () => {
      let total = 0
      for (const record of live)
        total += record.inFlight
      return total
    },
    endpoint: () => latest,
  }
}
