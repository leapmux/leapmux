import { mkdirSync } from 'node:fs'
import { join } from 'node:path'

export interface CursorEnvironmentOptions {
  /** The isolated HOME of the run. */
  homeDir: string
  /** The origin of the mock, which serves Cursor's own backend. */
  origin: string
  modelKey: string
}

/** Point the Cursor CLI at the mock's Cursor surface, with its configuration in the isolated HOME and no keychain. */
export function createCursorEnvironment(options: CursorEnvironmentOptions): Record<string, string> {
  // Cursor's config directory is ISOLATED like every other provider's. It used
  // to be the real home's, because Cursor was the one provider that still
  // needed a live account's stored credentials. It answers to the mock now, so
  // a test neither reads nor writes the developer's own Cursor configuration.
  const configDir = join(options.homeDir, '.cursor')
  mkdirSync(configDir, { recursive: true })
  return {
    CURSOR_CONFIG_DIR: configDir,
    // Cursor talks to its OWN backend, not a model API, so this points at the
    // mock's Cursor surface rather than at a model route. See
    // `./cursorSurface` for the three facts that shape it -- above all that
    // the startup calls answer all-defaults, because a REAL answer makes the
    // agent use its built-in endpoint and the turn never arrives here.
    CURSOR_API_ENDPOINT: options.origin,
    // The ACP entrypoint refuses `session/new` without a token, which
    // `cursor-agent --print` never asked for. The value is not checked
    // against anything -- the mock reads no credential -- but its ABSENCE is,
    // and it is refused locally, before any request reaches the endpoint.
    CURSOR_AUTH_TOKEN: options.modelKey,
    // Keep the CLI out of the developer's macOS keychain. Its default
    // credential store is the keychain, and a fresh `CURSOR_CONFIG_DIR` has
    // nothing to read, so the CLI asks for one and macOS raises a MODAL
    // dialog ("A keychain cannot be found to store cursor-user"). Nothing
    // answers it on a test machine, so the agent starts, blocks, and never
    // opens its turn stream -- which reads as a provider that hangs.
    // `memory` writes nothing anywhere: CURSOR_AUTH_TOKEN supplies the
    // credential on every spawn, so a test has nothing worth persisting, and
    // no stale credential can outlive the run that made it.
    AGENT_CLI_CREDENTIAL_STORE: 'memory',
  }
}
