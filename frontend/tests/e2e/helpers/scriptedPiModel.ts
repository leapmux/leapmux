import type { ServerInfo } from '../fixtures'
import { execFile } from 'node:child_process'
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { promisify } from 'node:util'
import { withCleanup } from './cleanup'
import { MODEL_KEY } from './mockAgentEnvironment'
import { assertPrivateNativePath } from './nativeCredentialIsolation'
import { getGlobalState, hubSpawnEnv } from './server'

type PiMockServer = Pick<ServerInfo, 'mockModelUrl' | 'agentEnv'>

/** Run a test with Pi project extensions enabled, then restore the prior trust decision. */
async function withTrustedPiDirectory<T>(directory: string, environment: NodeJS.ProcessEnv, run: () => Promise<T>): Promise<T> {
  mkdirSync(join(directory, '.pi', 'extensions'), { recursive: true })
  return withCleanup(async () => {
    await setFixtureTrust(directory, environment, true)
    return run()
  }, async () => {
    if (existsSync(join(directory, 'prior-project-trust.json')))
      await setFixtureTrust(directory, environment, false)
  })
}

/** Register the shared OpenAI-compatible mock as a trusted Pi project model. */
export async function withMockPiModel<T>(
  directory: string,
  server: PiMockServer,
  run: (settings: { model: string, optionValues: Record<string, string> }) => Promise<T>,
): Promise<T> {
  if (!server || typeof server.mockModelUrl !== 'string' || server.mockModelUrl === '')
    throw new Error('The Pi mock model requires the suite server URL.')
  const origin = new URL(server.mockModelUrl)
  if (origin.protocol !== 'http:' || !['127.0.0.1', 'localhost', '[::1]'].includes(origin.hostname) || origin.pathname !== '/')
    throw new Error('The Pi mock server URL must be a loopback HTTP origin')
  const nativeEnvironment = server.agentEnv
  if (!nativeEnvironment || typeof nativeEnvironment.HOME !== 'string' || nativeEnvironment.HOME === ''
    || typeof nativeEnvironment.PI_CODING_AGENT_DIR !== 'string' || nativeEnvironment.PI_CODING_AGENT_DIR === '') {
    throw new Error('The Pi mock model requires the isolated native environment.')
  }
  const runDir = getGlobalState().tmpDir
  assertPrivateNativePath(nativeEnvironment.HOME, runDir)
  assertPrivateNativePath(nativeEnvironment.PI_CODING_AGENT_DIR, runDir)
  assertPrivateNativePath(directory, runDir)
  const environment = hubSpawnEnv(nativeEnvironment)
  const provider = 'leapmux-control-test'
  const configuration = {
    baseUrl: `${origin.origin}/v1`,
    apiKey: MODEL_KEY,
    api: 'openai-completions',
    models: [{ id: 'probe', name: 'Protocol test', reasoning: false, input: ['text'], cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 128000, maxTokens: 1000 }],
  }
  const extensions = join(directory, '.pi', 'extensions')
  mkdirSync(extensions, { recursive: true })
  writeFileSync(join(extensions, 'protocol-model.ts'), `export default function (pi) { pi.registerProvider(${JSON.stringify(provider)}, ${JSON.stringify(configuration)}); }`)
  return withTrustedPiDirectory(directory, environment, () => run({ model: 'probe', optionValues: { pi_provider: provider, effort: 'off' } }))
}

/** Pi loads project extensions only after trust. Restore this temporary directory's exact entry. */
async function setFixtureTrust(directory: string, environment: NodeJS.ProcessEnv, enable: boolean): Promise<void> {
  const operation = join(directory, enable ? 'protocol-trust-enable.ts' : 'protocol-trust-restore.ts')
  const acknowledgement = join(directory, enable ? 'trust-enabled' : 'trust-restored')
  const previous = join(directory, 'prior-project-trust.json')
  writeFileSync(operation, `
import { getAgentDir, ProjectTrustStore } from '@earendil-works/pi-coding-agent';
import { readFileSync, realpathSync, writeFileSync } from 'node:fs';
import { resolve } from 'node:path';
export default function () {
  const directory = realpathSync(${JSON.stringify(directory)});
  const store = new ProjectTrustStore(getAgentDir());
  if (${JSON.stringify(enable)}) {
    const entry = store.getEntry(directory);
    const previous = entry && resolve(entry.path) === directory ? entry.decision : null;
    writeFileSync(${JSON.stringify(previous)}, JSON.stringify(previous));
    store.set(directory, true);
  } else {
    store.set(directory, JSON.parse(readFileSync(${JSON.stringify(previous)}, 'utf8')));
  }
  writeFileSync(${JSON.stringify(acknowledgement)}, 'ready');
  process.exit(0);
}
`)
  await promisify(execFile)('pi', ['--mode', 'rpc', '--extension', operation], { cwd: directory, timeout: 30_000, env: environment })
  if (!existsSync(acknowledgement) || readFileSync(acknowledgement, 'utf8') !== 'ready')
    throw new Error('Pi did not update trust for the protocol test directory.')
}
