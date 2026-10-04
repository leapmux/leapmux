import type { ManagedNativeScenarioContext } from './nativeScenario'
import { readFileSync, realpathSync } from 'node:fs'
import { homedir } from 'node:os'
import { isAbsolute, relative, sep } from 'node:path'
import { expect } from '@playwright/test'
import { sendNativeAnswer } from './nativeConversation'
import { getGlobalState } from './server'

/** Validate a native path against the directory that owns its test run. */
export function assertPrivateNativePath(path: string, runDir: string): void {
  if (!path || !runDir)
    throw new Error('A private native path and run directory must be nonempty.')
  const relativePath = relative(realpathSync(runDir), realpathSync(path))
  if (isAbsolute(relativePath) || relativePath === '..' || relativePath.startsWith(`..${sep}`))
    throw new Error('The private native path resolves outside the E2E run.')
}

/** Prove that an isolated native configuration routes a real turn to the suite mock. */
export async function exerciseCredentialIsolation(
  context: ManagedNativeScenarioContext,
  options: {
    configurationFiles?: readonly string[]
    inlineConfiguration?: readonly string[]
    privateDirectories: readonly string[]
    expectedCredential: string
    configurationMarkers?: readonly string[]
  },
): Promise<void> {
  const environment = context.leapmuxServer.agentEnv
  const mockUrl = context.leapmuxServer.mockModelUrl
  if (!environment || !mockUrl)
    throw new Error('The native credential proof requires the isolated environment and mock URL.')
  if (typeof environment.HOME !== 'string' || environment.HOME === '')
    throw new Error('The isolated native HOME must be a nonempty string.')
  if (!options.expectedCredential)
    throw new Error('The expected isolated native credential must be nonempty.')
  expect(realpathSync(environment.HOME)).not.toBe(realpathSync(homedir()))
  expect(options.privateDirectories.length).toBeGreaterThan(0)
  const runDir = getGlobalState().tmpDir
  for (const directory of options.privateDirectories) {
    assertPrivateNativePath(directory, runDir)
  }
  for (const file of options.configurationFiles ?? [])
    assertPrivateNativePath(file, runDir)
  const sources = [
    ...options.configurationFiles?.map(path => readFileSync(path, 'utf8')) ?? [],
    ...options.inlineConfiguration ?? [],
  ]
  expect(sources.length).toBeGreaterThan(0)
  const configuration = sources.join('\n')
  expect(configuration).toContain(new URL(mockUrl).origin)
  expect(configuration).toContain(options.expectedCredential)
  for (const marker of options.configurationMarkers ?? [])
    expect(configuration).toContain(marker)
  const request = await sendNativeAnswer(context, 'Reply through the isolated native model configuration.', 'The isolated native configuration reached the suite mock.')
  expect(request.mockCredential?.accepted, 'the native request must use an actual mock credential').toBe(true)
  expect(request.stepIndex).toBeDefined()
  expect(request.path).not.toBe('')
}
