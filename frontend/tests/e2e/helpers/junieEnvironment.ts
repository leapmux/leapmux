import { chmodSync, copyFileSync, mkdirSync, readdirSync, readFileSync, readlinkSync, rmSync, statSync, symlinkSync, writeFileSync } from 'node:fs'
import { basename, join } from 'node:path'
import process from 'node:process'
import { findBinary } from './binaryOnPath'
import { writePrivateJSON } from './privateConfigFile'
import { quotePosixShellArgument } from './shellArguments'

export interface JunieEnvironmentOptions {
  /** The run directory, which holds the model profiles, the child profiles, and the private data directory. */
  runDirectory: string
  /** The isolated HOME of the run. */
  homeDir: string
  /** The directory of private command wrappers, first on the run's PATH. */
  shimsDirectory: string
  /** The origin of the mock, which serves the OpenAI APIs. */
  origin: string
  modelKey: string
  /** The model that the endpoint receives from each mock profile. */
  modelID: string
  /** The profile that the test subagent runs on, such as `custom:mock-model`. */
  childModel: string
  /** The name of the private proxy that Junie routes its own requests through. */
  proxyProvider: string
  /** The developer's own HOME, where Junie installs its programs. Absent, Junie reads no install root. */
  realHomeDir: string | undefined
}

/**
 * Configure Junie's private store and explicit model and child locations.
 *
 * JUNIE_HOME contains private sessions and secrets under the isolated HOME.
 * Junie cannot read or write the user's store through that directory.
 *
 * JUNIE_DATA selects the installed programs in versions/ or current/.
 * The private HOME has no program installation, so the fixture selects the user's installed binaries.
 * Those program directories contain no session state.
 * See {@link junieDataDirectory} for why the directory is a private one that links them.
 *
 * JUNIE_SKIP_UPDATE_CHECK turns off the update check and download of the CLI itself.
 * It does not stop the launcher, which applies a staged update before the CLI starts.
 *
 * JUNIE_CONFIG_LOCATION selects the explicit configuration file for these locations:
 * - Mock model profiles.
 * - Custom child profiles.
 * - Private proxy endpoint.
 *
 * The Worker passes --model-default-locations=false and --agent-default-location=false.
 * Junie therefore does not scan default model or child directories, including project and JUNIE_HOME model folders.
 * The explicit fixture locations remain enabled under both flags.
 */
export function createJunieEnvironment(options: JunieEnvironmentOptions): Record<string, string> {
  const modelsDir = join(options.runDirectory, 'junie-models')
  const agentsDir = join(options.runDirectory, 'junie-agents')
  const junieHome = join(options.homeDir, '.junie')
  for (const directory of [modelsDir, agentsDir, junieHome])
    mkdirSync(directory, { recursive: true })
  writePrivateJSON(join(modelsDir, 'mock-model.json'), junieModelProfile(options, `${options.origin}/v1/chat/completions`))
  writePrivateJSON(join(modelsDir, 'mock-responses.json'), junieModelProfile(options, `${options.origin}/v1/responses`, 'OpenAIResponses'))
  writeFileSync(join(agentsDir, 'leapmux-e2e-child.md'), junieTestSubagent(options.childModel), { mode: 0o600 })
  const configPath = join(modelsDir, 'config.json')
  writePrivateJSON(configPath, {
    'model-locations': [modelsDir],
    'agent-locations': [agentsDir],
    'provider': options.proxyProvider,
    'proxies': [{
      'name': options.proxyProvider,
      'kind': 'OpenAI',
      'api-url': options.origin,
      'headers': [`Authorization: Bearer ${options.modelKey}`],
    }],
  })
  writeJunieWrapper(options.shimsDirectory)
  const env: Record<string, string> = {
    JUNIE_HOME: junieHome,
    JUNIE_CONFIG_LOCATION: configPath,
    JUNIE_SKIP_UPDATE_CHECK: '1',
  }
  const installRoot = options.realHomeDir === undefined ? undefined : join(options.realHomeDir, '.local', 'share', 'junie')
  if (installRoot !== undefined)
    env.JUNIE_DATA = junieDataDirectory(installRoot, join(options.runDirectory, 'junie-data'))
  return env
}

/**
 * Build a Junie custom model profile in an explicit model location.
 *
 * --model-location or model-locations selects the folder that contains each JSON profile.
 * The file basename selects the profile: mock-model.json gives custom:mock-model.
 * id supplies the model identifier in the request. baseUrl supplies the complete endpoint for either API type.
 */
function junieModelProfile(options: JunieEnvironmentOptions, fullEndpoint: string, apiType: 'OpenAICompletion' | 'OpenAIResponses' = 'OpenAICompletion'): Record<string, unknown> {
  return {
    id: options.modelID,
    displayName: apiType === 'OpenAIResponses' ? 'Mock Responses Model' : 'Mock Model',
    providerName: 'Mock',
    baseUrl: fullEndpoint,
    apiKey: options.modelKey,
    apiType,
    maxContextLength: 200000,
  }
}

/** A child with a file-read turn before its final answer. */
function junieTestSubagent(childModel: string): string {
  return `---
name: leapmux-e2e-child
description: Read a local marker file and report its contents in an isolated test.
model: ${childModel}
---

You are the LeapMux test subagent.
Read the file in the task with open_entire_file.
Then call submit with the marker you read.
`
}

/**
 * Put a private `junie` wrapper first on the run's PATH.
 *
 * The Worker finds Junie through PATH. The wrapper puts the private wrapper directory
 * first on PATH again, because macOS zsh rebuilds PATH in its system login profile,
 * and the credential-store wrapper in that directory keeps Junie out of the keychain.
 * Playwright's availability check still finds the installed CLI in its own process.
 * Windows uses the Win32 credential manager, which PATH cannot replace, so Windows
 * gets no wrapper.
 */
function writeJunieWrapper(shimsDirectory: string): void {
  if (process.platform === 'win32')
    return
  const realJunie = findBinary('junie')
  if (realJunie === null)
    return
  // The CLI refreshes the launcher that JUNIE_SHIM_PATH names, which is the file that
  // started it, when the launcher is older than the one in the installed version. Run
  // a private copy of the launcher script, so that refresh cannot rewrite the
  // developer's own ~/.local/bin/junie. The launcher reads no path of its own: it
  // takes JUNIE_DATA from the environment.
  const launcher = junieLauncherCopy(realJunie, join(shimsDirectory, 'junie-launcher'))
  writeFileSync(join(shimsDirectory, 'junie'), `#!/bin/sh\nexport PATH=${quotePosixShellArgument(shimsDirectory)}:"$PATH"\nexec ${quotePosixShellArgument(launcher)} "$@"\n`, { mode: 0o755 })
}

/** The size above which an installed `junie` is a program, not the launcher script. */
const JUNIE_LAUNCHER_MAX_BYTES = 1 << 20

/**
 * The path that the E2E Junie launcher runs: a private copy when the installed file is the
 * managed launcher script, and the installed file itself otherwise.
 */
function junieLauncherCopy(installed: string, copy: string): string {
  let script: string
  try {
    // The launcher script is some 30 KB. A larger file is a program, not the script.
    if (statSync(installed).size > JUNIE_LAUNCHER_MAX_BYTES)
      return installed
    script = readFileSync(installed, 'utf8')
  }
  catch {
    return installed
  }
  if (!script.startsWith('#!') || !script.includes('JUNIE_MANAGED_SHIM'))
    return installed
  copyFileSync(installed, copy)
  chmodSync(copy, 0o755)
  return copy
}

/**
 * The data directory that the Junie launcher runs against, or the install root when
 * the root has no layout that a private directory can link.
 *
 * The launcher applies `updates/pending-update.json` before it starts the CLI: it
 * swaps `versions/<version>` and flips the `current` link. An interactive Junie stages
 * that file on its own, so a run that shares the operator's data directory applies the
 * operator's update in the middle of a test run, and `--skip-update-check` does not
 * stop it.
 *
 * The private directory links each installed version, points its own `current` link at
 * the version that the install runs, and holds an empty `updates/`. The launcher then
 * finds nothing to apply, and a swap that it makes would move only a link in the private
 * directory. The versions stay real, so no program is copied.
 */
function junieDataDirectory(installRoot: string, dataDir: string): string {
  // The launcher is a bash script, and a link to a directory needs a privilege on Windows.
  if (process.platform === 'win32')
    return installRoot
  const versionsDir = join(installRoot, 'versions')
  let installed: string[]
  let current: string
  try {
    installed = readdirSync(versionsDir, { withFileTypes: true }).filter(entry => entry.isDirectory() && !entry.name.startsWith('.')).map(entry => entry.name)
    current = basename(readlinkSync(join(installRoot, 'current')))
  }
  catch {
    return installRoot
  }
  if (!installed.includes(current))
    return installRoot
  // A second call for the same run directory builds the directory again. Removing a
  // directory of links deletes the links, never the versions that they name.
  rmSync(dataDir, { recursive: true, force: true })
  const privateVersions = join(dataDir, 'versions')
  mkdirSync(privateVersions, { recursive: true })
  mkdirSync(join(dataDir, 'updates'), { recursive: true })
  for (const version of installed)
    symlinkSync(join(versionsDir, version), join(privateVersions, version), 'dir')
  symlinkSync(join(privateVersions, current), join(dataDir, 'current'), 'dir')
  return dataDir
}
