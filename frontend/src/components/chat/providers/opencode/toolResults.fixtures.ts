export type OpenCodeToolContent = { type: 'text', text: string } | { type: 'file', uri: string, mime: string }

type NativeToolState
  = | { status: 'running', metadata?: Record<string, unknown> }
    | { status: 'completed', content: OpenCodeToolContent[], metadata?: Record<string, unknown> }
    | { status: 'error', error: Record<string, unknown>, content?: OpenCodeToolContent[], metadata?: Record<string, unknown> }

/** Native projected parts retain their input on both sides of a Worker span. */
export function openCodeToolPart(id: string, name: string, input: Record<string, unknown>, state: NativeToolState) {
  return {
    type: 'tool',
    id,
    name,
    time: { created: 0 },
    state: { input, metadata: {}, ...state },
  }
}
