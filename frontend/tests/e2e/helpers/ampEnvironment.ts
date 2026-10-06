export interface AmpEnvironmentOptions {
  /** The origin of the mock, which serves Amp's own service in ./ampSurface. */
  origin: string
  modelKey: string
}

/**
 * Direct Amp to its native service in ./ampSurface.
 *
 * The Surface controls Amp's actor loop and supplies each scripted inference.
 * Amp stores its login under ~/.local/share/amp in the private HOME, which the run creates empty.
 * Its only available credential is the fixed mock key.
 * A request that reaches the real service cannot authenticate with that key.
 * Amp reads its settings, its history, and its cache under the XDG base directories, which the run sets for every
 * agent.
 */
export function createAmpEnvironment(options: AmpEnvironmentOptions): Record<string, string> {
  return {
    AMP_URL: options.origin,
    AMP_API_KEY: options.modelKey,
    // The actor gateway; the CLI would derive the same address from AMP_URL.
    RIVET_PUBLIC_ENDPOINT: `${options.origin}/actors`,
    // Empty, which Amp reads as unset, so a developer's own pool cannot reach it.
    RIVET_POOL: '',
    // Empty for the same reason. The worker hands Amp a settings file of its own,
    // built from the user settings under the isolated XDG_CONFIG_HOME.
    AMP_SETTINGS_FILE: '',
    AMP_SKIP_UPDATE_CHECK: '1',
    AMP_REMOTE_CONTROL_TERMINAL: '0',
  }
}
