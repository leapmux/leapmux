/**
 * Dev-only CustomEvent emission for e2e timing tests.
 *
 * Calls are gated on `import.meta.env.LEAPMUX_DEV` so the bundler can
 * dead-code-eliminate them in production builds (cost is zero).
 *
 * The e2e test `121-claude-agent-open-timing.spec.ts` listens on
 * `leapmux:rpc-send` / `leapmux:rpc-recv` to measure handler latency.
 */
export function emitDevEvent(name: string, detail: Record<string, unknown>): void {
  if (import.meta.env.LEAPMUX_DEV && typeof window !== 'undefined')
    window.dispatchEvent(new CustomEvent(name, { detail }))
}
