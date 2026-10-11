import { RUN_ROOT_PREFIX } from './runRoot'

/**
 * The guidance files that Amp ignores: each file directly in a run root (./runRoot.ts), which holds the sentinel
 * instruction files of ./ancestorInstructions.ts.
 *
 * Amp reads the guidance files of each directory from its working directory up to the root of the file system.
 * `AMP_IGNORE_GUIDANCE_FILES` holds one glob that Amp matches against the absolute path of each file. Amp
 * 0.0.1791074829 states no such variable in its help. A native probe of that version showed it:
 *
 * - With no value, Amp sent the `AGENTS.md` of its working directory and the sentinel `AGENTS.md` of the directory
 *   above it.
 * - A value of `1` or `true` changed nothing.
 * - `<root>/AGENTS.md` and `<root>/*` removed the sentinel and kept the file of the working directory.
 * - A glob that matched each `AGENTS.md` in any directory removed both.
 * - A comma between two globs matched neither glob.
 *
 * The glob matches the run root by its name, because the environment is fixed for the whole run and a run root takes
 * a new name in each run. It also matches the files directly in any other directory whose name starts the same way,
 * and no working directory of a spec has such a name. A glob matcher that skips a dot segment with `**`, as Node's
 * own does, misses a run root whose parent path holds one, such as a `LEAPMUX_E2E_RUN_PARENT` under `.tmp`. No probe
 * tested Amp's matcher there. If it misses, the check of the Amp surface refuses the sentinel that Amp then sends, so
 * the run fails visibly.
 */
export const AMP_IGNORED_GUIDANCE_FILES = `**/${RUN_ROOT_PREFIX}*/*`

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
    AMP_IGNORE_GUIDANCE_FILES: AMP_IGNORED_GUIDANCE_FILES,
  }
}
