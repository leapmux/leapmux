/**
 * Dev-only CustomEvent emission for e2e timing tests.
 *
 * The detail is a thunk so `performance.now()` and object literals at the
 * call site don't run in production — the bundler dead-code-eliminates the
 * whole body when `import.meta.env.LEAPMUX_DEV` is false, leaving only the
 * unused thunk reference (which the JIT elides too).
 *
 * The e2e test `121-claude-agent-open-timing.spec.ts` listens on
 * `leapmux:rpc-send` / `leapmux:rpc-recv` to measure handler latency.
 */
export function emitDevEvent(name: string, detail: () => Record<string, unknown>): void {
  if (import.meta.env.LEAPMUX_DEV && typeof window !== 'undefined')
    window.dispatchEvent(new CustomEvent(name, { detail: detail() }))
}
