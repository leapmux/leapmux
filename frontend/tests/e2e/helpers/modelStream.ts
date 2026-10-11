import type { ServerResponse } from 'node:http'
import type { MockModelStep, MockModelTextStream } from './mockModelScript'
import { waitUnlessDisconnected } from './mockHttp'
import { textChunks } from './mockModelScript'

export interface ModelStream {
  readonly active: boolean
  chunks: (text: string | undefined) => AsyncGenerator<string>
}

/** Consume generation chunks before a service sends its complete response. */
export async function bufferModelOutput(stream: ModelStream, step: Pick<MockModelStep, 'reasoning' | 'text'>): Promise<boolean> {
  for (const text of [step.reasoning, step.text]) {
    for await (const _chunk of stream.chunks(text)) {
      if (!stream.active)
        return false
    }
  }
  return stream.active
}

/** Count emitted chunks across reasoning and text. Stop when the client leaves. */
export function createModelStream(
  response: ServerResponse,
  stream?: MockModelTextStream,
  hold?: (name: string) => Promise<boolean>,
): ModelStream {
  return createControlledModelStream(stream, hold, {
    active: () => !response.writableEnded && !response.destroyed,
    pause: () => waitUnlessDisconnected(stream?.delayMs ?? 0, { response }),
  })
}

/** Generate internal chunks while a native actor buffers its complete message. */
export function createBufferedModelStream(
  signal: AbortSignal,
  stream?: MockModelTextStream,
  hold?: (name: string) => Promise<boolean>,
): ModelStream {
  return createControlledModelStream(stream, hold, {
    active: () => !signal.aborted,
    pause: () => waitUnlessDisconnected(stream?.delayMs ?? 0, { signal }),
  })
}

function createControlledModelStream(
  stream: MockModelTextStream | undefined,
  hold: ((name: string) => Promise<boolean>) | undefined,
  control: { active: () => boolean, pause: () => Promise<boolean> },
): ModelStream {
  if (stream?.gates?.length && !hold)
    throw new Error('A model stream gate requires a scenario-owned gate handler')
  let emitted = 0
  let cancelled = false
  const active = () => !cancelled && control.active()
  return {
    get active() { return active() },
    async* chunks(text) {
      if (text === undefined)
        return
      for (const chunk of textChunks({ text, ...(stream ? { stream } : {}) })) {
        if (!active())
          return
        if (emitted > 0 && !await control.pause())
          cancelled = true
        if (!active())
          return
        yield chunk
        emitted++
        const gate = stream?.gates?.find(gate => gate.afterChunk === emitted)
        if (gate) {
          if (!hold)
            throw new Error('A model stream gate requires a scenario-owned gate handler')
          if (!await hold(gate.name)) {
            cancelled = true
            return
          }
        }
      }
    },
  }
}
