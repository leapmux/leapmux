import type { MessageCompletion } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeOutputSnapshot } from '../helpers/nativeOutputReaderCases'

/** Native v2 success events omit the name and arguments from their start records. */
export function openCodeToolFrame(type = 'session.tool.success', data: Record<string, unknown> = {}) {
  return {
    type,
    data: {
      sessionID: 'native-session',
      assistantMessageID: 'msg_1',
      id: 'native-call',
      content: [
        { type: 'text', text: 'native preview' },
      ],
      metadata: { exit: 0, outputPath: '/native/shell/output' },
      ...data,
    },
  }
}

export function openCodeToolSnapshot(frame: unknown = openCodeToolFrame(), completion?: MessageCompletion) {
  return nativeOutputSnapshot([
    {
      frame,
      spanId: 'worker-span',
      ...(completion === undefined
        ? {}
        : { completion }),
    },
  ])
}
