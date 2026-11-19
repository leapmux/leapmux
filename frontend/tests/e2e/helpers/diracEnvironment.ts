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

/** Enable the native capabilities exercised by the isolated Dirac profiles. */
export function diracTestState() {
  return {
    telemetrySetting: 'disabled',
    autoApproveAllToggled: true,
    yoloModeToggled: true,
    subagentsEnabled: true,
  }
}

/** Disable automatic approval in the isolated profile used for permission tests. */
export function diracAskingState(state: Record<string, unknown>): Record<string, unknown> {
  return {
    ...state,
    autoApproveAllToggled: false,
    yoloModeToggled: false,
    // Dirac 0.6.0 treats enabled as a legacy field. Individual actions control
    // ordinary approvals, and executeCommands defaults to true when absent.
    autoApprovalSettings: {
      version: 1,
      enabled: true,
      favorites: [],
      maxRequests: 20,
      actions: {
        readFiles: false,
        readFilesExternally: false,
        editFiles: false,
        editFilesExternally: false,
        executeCommands: false,
        useBrowser: false,
        applyDiff: false,
      },
      enableNotifications: false,
    },
  }
}

/** Point Dirac at the mock through its OpenAI provider, with telemetry off, every approval granted, and no update. */
export function createDiracEnvironment(options: DiracEnvironmentOptions): Record<string, string> {
  const diracDir = join(options.homeDir, '.dirac')
  mkdirSync(join(diracDir, 'data', 'state'), { recursive: true })
  writeFileSync(join(diracDir, 'data', 'globalState.json'), JSON.stringify(diracTestState()), { mode: 0o600 })
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
