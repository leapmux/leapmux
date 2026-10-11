/**
 * Emit development events for end-to-end timing tests.
 *
 * The detail function defers the timestamp and event data.
 * The production build removes this branch when import.meta.env.LEAPMUX_DEV is false.
 * The just-in-time compiler removes the unused function reference.
 *
 * tests/e2e/claude-code/agent-startup.spec.ts listens for these events to measure handler latency:
 * - leapmux:rpc-send.
 * - leapmux:rpc-recv.
 */
export function emitDevEvent(name: string, detail: () => Record<string, unknown>): void {
  if (import.meta.env.LEAPMUX_DEV && typeof window !== 'undefined')
    window.dispatchEvent(new CustomEvent(name, { detail: detail() }))
}
