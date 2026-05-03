// Buffer-then-flush adapter for an underlying stream listener — see
// bufferStreamHandle below for the full rationale.

export interface StreamSource<TMessage> {
  onMessage: (cb: (msg: TMessage) => void) => void
  onEnd: (cb: () => void) => void
  onError: (cb: (err: Error) => void) => void
}

export interface BufferedStreamHandle<TEvent> {
  onEvent: (cb: (event: TEvent) => void) => void
  onEnd: (cb: () => void) => void
  onError: (cb: (err: Error) => void) => void
}

/**
 * Wrap a raw stream source with buffering plus a `decode` step that
 * converts low-level messages into typed events. Any backend event
 * arriving before the consumer wires its callbacks is captured and
 * flushed on first registration, so events emitted exactly when a
 * stream is being (re)established aren't silently dropped.
 *
 * Each flush is one-shot: re-registering a callback replaces the
 * destination but does not re-deliver already-flushed events.
 *
 * If `decode` throws, the error is routed through onError (buffered if
 * the consumer hasn't registered yet).
 */
export function bufferStreamHandle<TMessage, TEvent>(
  source: StreamSource<TMessage>,
  decode: (msg: TMessage) => TEvent,
): BufferedStreamHandle<TEvent> {
  let eventCb: ((event: TEvent) => void) | null = null
  let endCb: (() => void) | null = null
  let errorCb: ((err: Error) => void) | null = null

  const pendingEvents: TEvent[] = []
  let pendingEnd = false
  let pendingError: Error | null = null

  source.onMessage((msg) => {
    let event: TEvent
    try {
      event = decode(msg)
    }
    catch (err) {
      const e = err instanceof Error ? err : new Error(String(err))
      if (errorCb)
        errorCb(e)
      else
        pendingError = e
      return
    }
    if (eventCb)
      eventCb(event)
    else
      pendingEvents.push(event)
  })

  source.onEnd(() => {
    if (endCb)
      endCb()
    else
      pendingEnd = true
  })

  source.onError((err) => {
    if (errorCb)
      errorCb(err)
    else
      pendingError = err
  })

  return {
    onEvent(cb) {
      eventCb = cb
      if (pendingEvents.length > 0) {
        const buffered = pendingEvents.splice(0, pendingEvents.length)
        for (const event of buffered) cb(event)
      }
    },
    onEnd(cb) {
      endCb = cb
      if (pendingEnd) {
        pendingEnd = false
        cb()
      }
    },
    onError(cb) {
      errorCb = cb
      if (pendingError) {
        const err = pendingError
        pendingError = null
        cb(err)
      }
    },
  }
}
