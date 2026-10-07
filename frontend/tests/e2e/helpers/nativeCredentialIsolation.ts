import type { ManagedNativeScenarioContext } from './nativeScenario'
import { existsSync, readFileSync, realpathSync } from 'node:fs'
import { homedir } from 'node:os'
import { isAbsolute, relative, sep } from 'node:path'
import { expect } from '@playwright/test'
import { sendNativeAnswer } from './nativeConversation'
import { getGlobalState } from './server'

/**
 * Validate a native path against the directory that owns its test run.
 * Both paths must exist. The file system resolves each path through its symbolic links.
 */
export function assertPrivateNativePath(path: string, runDir: string): void {
  if (!path || !runDir)
    throw new Error('A private native path and run directory must be nonempty.')
  if (!existsSync(runDir))
    throw new Error(`The E2E run directory ${runDir} does not exist.`)
  if (!existsSync(path))
    throw new Error(`The private native path ${path} does not exist.`)
  // The JavaScript resolver normalizes parent segments before symbolic links.
  // The native resolver preserves the file system order for a link followed by `..`.
  const relativePath = relative(realpathSync.native(runDir), realpathSync.native(path))
  if (isAbsolute(relativePath) || relativePath === '..' || relativePath.startsWith(`..${sep}`))
    throw new Error('The private native path resolves outside the E2E run.')
}

/** What the text of an isolated native configuration must state and must not state. */
export interface IsolatedConfigurationRules {
  /** The origin of the suite mock. The configuration must point at it. */
  mockOrigin: string
  /** A credential that the configuration states. */
  expectedCredential?: string | undefined
  /**
   * Text that the configuration must not hold, such as a key that the CLI reads from an environment variable.
   * The accepted mock credential of the turn request proves that key.
   */
  absentFromConfiguration?: readonly string[] | undefined
  /** Text that the configuration must hold, such as the line that specifies the environment variable. */
  configurationMarkers?: readonly string[] | undefined
}

/**
 * Check the text of an isolated native configuration.
 * State the required credential or the text that the configuration must not hold.
 * A value that the test copies into the configuration cannot fail the first check.
 */
export function assertIsolatedConfiguration(configuration: string, rules: IsolatedConfigurationRules): void {
  const absent = rules.absentFromConfiguration ?? []
  if (rules.expectedCredential === undefined && absent.length === 0)
    throw new Error('The native configuration rules must state its credential: an expected credential or text that it must not hold.')
  if (rules.expectedCredential === '')
    throw new Error('The expected isolated native credential must be nonempty.')
  if (absent.includes(''))
    throw new Error('Text that the native configuration must not hold must be nonempty.')
  if (!configuration.includes(rules.mockOrigin))
    throw new Error(`The native configuration does not point at the suite mock ${rules.mockOrigin}.`)
  if (rules.expectedCredential !== undefined && !configuration.includes(rules.expectedCredential))
    throw new Error('The native configuration does not state the expected credential.')
  for (const text of absent) {
    if (configuration.includes(text))
      throw new Error('The native configuration holds text that it must not hold.')
  }
  for (const marker of rules.configurationMarkers ?? []) {
    if (!configuration.includes(marker))
      throw new Error(`The native configuration lacks the marker ${marker}.`)
  }
}

/** Prove that an isolated native configuration routes a real turn to the suite mock. */
export async function exerciseCredentialIsolation(
  context: ManagedNativeScenarioContext,
  options: {
    configurationFiles?: readonly string[]
    inlineConfiguration?: readonly string[]
    privateDirectories: readonly string[]
    /**
     * The credential that the configuration states.
     * Use `absentFromConfiguration` instead when the CLI keeps no key in its files.
     */
    expectedCredential?: string
    absentFromConfiguration?: readonly string[]
    configurationMarkers?: readonly string[]
  },
): Promise<void> {
  // An unset environment variable reaches here as undefined.
  // A configuration join can turn it into an empty string, which hides the absent value.
  if (options.inlineConfiguration?.some(value => typeof value !== 'string' || value === ''))
    throw new Error('Each inline native configuration must be a nonempty string.')
  if (options.privateDirectories.some(value => typeof value !== 'string' || value === ''))
    throw new Error('Each private directory must be a nonempty path.')
  const environment = context.leapmuxServer.agentEnv
  const mockUrl = context.leapmuxServer.mockModelUrl
  if (!environment || !mockUrl)
    throw new Error('The native credential proof requires the isolated environment and mock URL.')
  if (typeof environment.HOME !== 'string' || environment.HOME === '')
    throw new Error('The isolated native HOME must be a nonempty string.')
  if (!existsSync(environment.HOME))
    throw new Error(`The isolated native HOME ${environment.HOME} does not exist.`)
  expect(realpathSync(environment.HOME)).not.toBe(realpathSync(homedir()))
  expect(options.privateDirectories.length).toBeGreaterThan(0)
  const runDir = getGlobalState().tmpDir
  for (const directory of options.privateDirectories) {
    // A CLI creates its directory at its first write.
    // The suite creates each directory that its environment specifies before the test starts.
    if (!existsSync(directory))
      throw new Error(`The private directory ${directory} does not exist before the native turn. The suite environment (helpers/mockAgentEnvironment.ts) must create each directory that one of its variables gives.`)
    assertPrivateNativePath(directory, runDir)
  }
  for (const file of options.configurationFiles ?? [])
    assertPrivateNativePath(file, runDir)
  const sources = [
    ...options.configurationFiles?.map(path => readFileSync(path, 'utf8')) ?? [],
    ...options.inlineConfiguration ?? [],
  ]
  expect(sources.length).toBeGreaterThan(0)
  assertIsolatedConfiguration(sources.join('\n'), {
    mockOrigin: new URL(mockUrl).origin,
    expectedCredential: options.expectedCredential,
    absentFromConfiguration: options.absentFromConfiguration,
    configurationMarkers: options.configurationMarkers,
  })
  const request = await sendNativeAnswer(context, 'Reply through the isolated native model configuration.', 'The isolated native configuration reached the suite mock.')
  expect(request.mockCredential?.accepted, 'the native request must use an actual mock credential').toBe(true)
  expect(request.stepIndex).toBeDefined()
  expect(request.path).not.toBe('')
}
