import { mkdirSync } from 'node:fs'
import { join } from 'node:path'

export interface CopilotEnvironmentOptions {
  /** The isolated HOME of the run. */
  homeDir: string
  /** The origin of the mock, which serves the Copilot API and the GitHub API that Copilot calls. */
  origin: string
  modelKey: string
  /** The GitHub token that Copilot exchanges at the mock. */
  githubToken: string
}

/** Point GitHub Copilot CLI at the mock, with its state in the isolated HOME and its updater off. */
export function createCopilotEnvironment(options: CopilotEnvironmentOptions): Record<string, string> {
  const copilotHome = join(options.homeDir, '.copilot')
  mkdirSync(copilotHome, { recursive: true })
  return {
    COPILOT_API_URL: options.origin,
    COPILOT_DEBUG_GITHUB_API_URL: options.origin,
    COPILOT_GITHUB_TOKEN: options.githubToken,
    COPILOT_HOME: copilotHome,
    GITHUB_COPILOT_API_TOKEN: options.modelKey,
    // Copilot CLI starts its updater one second after every start, `--server
    // --stdio` included. The updater downloads the newest package, and then the
    // release executable, and renames that over the executable that runs, which is
    // the developer's own install: an isolated HOME does not move it. The refusing
    // proxy only fails the download, and it ends when a run starts without
    // it. Only the exact value `false` counts; `0` and `off` do not.
    COPILOT_AUTO_UPDATE: 'false',
  }
}
