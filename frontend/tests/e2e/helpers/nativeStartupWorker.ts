import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { NativeStartupLaunch, NativeStartupWrapper, NativeStartupWrapperOptions } from './nativeStartupWrapper'
import type { NativeWorker } from './nativeWorker'
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { delimiter, join } from 'node:path'
import { withCleanup } from './cleanup'
import { createNativeStartupWrapper } from './nativeStartupWrapper'
import { withNativeWorker } from './nativeWorker'
import { createTestDirectory } from './runDirectory'
import { hubSpawnEnv } from './server'
import { quotePosixShellArgument } from './shellArguments'

/** Restore the wrapper prefix after a login shell's system profile changes PATH. */
export function nativeStartupShellEnvironment(
  directory: string,
  wrapperDirectory: string,
  environment: NodeJS.ProcessEnv,
): NodeJS.ProcessEnv {
  mkdirSync(directory, { recursive: true })
  const path = `${wrapperDirectory}${delimiter}${environment.PATH ?? ''}`
  for (const file of ['.zlogin', '.zshrc']) {
    const original = environment.ZDOTDIR ? join(environment.ZDOTDIR, file) : environment.HOME ? join(environment.HOME, file) : undefined
    const source = original && existsSync(original) ? readFileSync(original, 'utf8') : ''
    writeFileSync(join(directory, file), `${source}\nexport PATH=${quotePosixShellArgument(wrapperDirectory)}:"$PATH"\n`, { mode: 0o600 })
  }
  return { PATH: path, ZDOTDIR: directory }
}

/** Connect one controlled Worker to the suite Hub with only isolated agent credentials. */
export async function withNativeStartupWorker(
  context: ManagedNativeScenarioContext,
  launch: NativeStartupLaunch,
  options: NativeStartupWrapperOptions & { workerEnvironment?: (wrapper: NativeStartupWrapper) => NodeJS.ProcessEnv },
  use: (workerId: string, wrapper: NativeStartupWrapper, worker: NativeWorker<ManagedNativeScenarioContext['leapmuxServer']>) => Promise<void>,
): Promise<void> {
  const server = context.leapmuxServer
  if (!server.agentEnv)
    throw new Error('The controlled startup Worker requires the isolated agent environment.')
  const wrapper = await createNativeStartupWrapper(createTestDirectory('native-startup-wrapper-'), launch, options)
  await withCleanup(async () => {
    const shellEnvironment = nativeStartupShellEnvironment(createTestDirectory('native-startup-shell-'), wrapper.directory, hubSpawnEnv(server.agentEnv))
    await withNativeWorker(server, {
      dataDirPrefix: 'native-startup-worker',
      workerName: 'Native startup test',
      env: { ...shellEnvironment, ...options.workerEnvironment?.(wrapper) },
    }, worker => use(worker.workerId, wrapper, worker))
  }, () => wrapper.dispose())
}
