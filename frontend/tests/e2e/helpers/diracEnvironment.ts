import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

export interface DiracEnvironmentOptions {
  /** The isolated HOME of the run. */
  homeDir: string
  /** The OpenAI-compatible base URL of the mock, which ends in `/v1`. */
  baseURL: string
  modelKey: string
  modelID: string
}

/** Point Dirac at the mock through its OpenAI provider, with telemetry off, every approval granted, and no update. */
export function createDiracEnvironment(options: DiracEnvironmentOptions): Record<string, string> {
  const diracDir = join(options.homeDir, '.dirac')
  mkdirSync(join(diracDir, 'data', 'state'), { recursive: true })
  writeFileSync(join(diracDir, 'data', 'globalState.json'), JSON.stringify({ telemetrySetting: 'disabled', autoApproveAllToggled: true, yoloModeToggled: true }), { mode: 0o600 })
  return {
    DIRAC_PROVIDER: 'openai',
    DIRAC_BASE_URL: options.baseURL,
    DIRAC_API_KEY: options.modelKey,
    DIRAC_MODEL: options.modelID,
    DIRAC_DIR: diracDir,
    // Dirac starts a detached update of its own install from its startup path,
    // `--acp` included. Only the exact value `1` counts. The worker pins it too.
    DIRAC_NO_AUTO_UPDATE: '1',
  }
}
