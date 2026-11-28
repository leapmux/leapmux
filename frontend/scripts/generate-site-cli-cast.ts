/**
 * Deterministic CLI asciicast v2 generator for the "Script your fleet from the CLI"
 * showcase on https://leapmux.dev.
 *
 * Boots a real isolated LeapMux Hub + Worker + local mock model server
 * (`./leapmux`), creates a fixture Git repository (`payments-api`), runs the
 * real `leapmux` CLI commands (`agent open --worktree`, `agent send`,
 * `agent wait --any --json`, and `agent messages --last 1`), captures their
 * real stdout, and writes `site/static/casts/cli-fleet-demo.cast`.
 *
 * Run explicitly when CLI output or the demo script changes:
 *   task generate-site-cli-cast
 */

import { execFile, execFileSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, realpathSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'
import {
  callHub,
  closeTestChannels,
  createWorkspaceViaAPI,
  elevateSessionViaAPI,
  getCurrentUser,
  TEST_ADMIN_PASSWORD,
} from '../tests/e2e/helpers/api'
import { finishCleanup } from '../tests/e2e/helpers/cleanup'
import { extendMockModelScenario } from '../tests/e2e/helpers/mockModelScenario'
import { AMBIENT_SCENARIO_ID } from '../tests/e2e/helpers/mockModelScript'
import { startSuiteServer } from '../tests/e2e/helpers/suiteServer'
import { initGitRepo } from '../tests/e2e/helpers/worktree'
import { AsciicastWriter, colorizeJsonAnsi, sanitizeCastText } from './cliCastWriter'

const execFileAsync = promisify(execFile)
const SCRIPT_DIR = dirname(fileURLToPath(import.meta.url))
const ROOT = resolve(SCRIPT_DIR, '../..')
const BINARY_PATH = resolve(
  ROOT,
  process.platform === 'win32'
    ? 'leapmux.exe'
    : 'leapmux',
)
const OUT_PATH = resolve(ROOT, 'site/static/casts/cli-fleet-demo.cast')

function git(cwd: string, ...args: string[]): void {
  execFileSync('git', args, {
    cwd,
    stdio: 'pipe',
    env: {
      ...process.env,
      GIT_AUTHOR_NAME: 'Alex Rivera',
      GIT_AUTHOR_EMAIL: 'alex@acme.dev',
      GIT_COMMITTER_NAME: 'Alex Rivera',
      GIT_COMMITTER_EMAIL: 'alex@acme.dev',
    },
  })
}

function createPaymentsRepo(runDir: string): string {
  const repoDir = join(runDir, 'repos', 'payments-api')
  mkdirSync(repoDir, { recursive: true })
  initGitRepo(repoDir)
  writeFileSync(join(repoDir, 'go.mod'), 'module acme.dev/payments\n\ngo 1.24\n', 'utf8')
  writeFileSync(join(repoDir, 'README.md'), '# payments-api\n\nCheckout and webhook service.\n', 'utf8')
  git(repoDir, 'add', '.')
  git(repoDir, 'commit', '-m', 'chore: initial payments-api repo')
  return repoDir
}

function hubHostForURL(hubURL: string): string {
  const parsed = new URL(hubURL)
  const scheme = parsed.protocol.replace(/:$/, '').toLowerCase() || 'https'
  const host = parsed.hostname.toLowerCase()
  return parsed.port
    ? `${scheme}_${host}_${parsed.port}`
    : `${scheme}_${host}`
}

async function mintCLIConfigInDir(
  hubURL: string,
  cookie: string,
  runDir: string,
): Promise<string> {
  const scopes = [
    'account:read',
    'workspace:read',
    'workspace:write',
    'worker:read',
    'agent:read',
    'agent:write',
    'git:read',
    'git:write',
  ]
  const user = await getCurrentUser(hubURL, cookie)
  if (!user.id || !user.username)
    throw new Error('mintCLIConfigInDir: missing admin user id or username')
  await elevateSessionViaAPI(hubURL, cookie, TEST_ADMIN_PASSWORD)
  const minted = await callHub<{ accessToken?: string }>(hubURL, 'AdminUserService/IssueAPIToken', {
    user: { id: user.id },
    installationName: `site-cast-${Date.now()}`,
    ttlSeconds: '3600',
    scopes,
  }, { cookie, operation: 'IssueAPIToken' })
  if (!minted.accessToken)
    throw new Error('mintCLIConfigInDir: no accessToken in response')

  const configDir = join(runDir, 'cli-cfg')
  const credsDir = join(configDir, 'credentials')
  mkdirSync(credsDir, { recursive: true })
  const credPath = join(credsDir, `${hubHostForURL(hubURL)}.json`)
  writeFileSync(
    credPath,
    JSON.stringify(
      {
        hub_url: hubURL,
        hub_id: 'site-cast',
        access_token: minted.accessToken,
        refresh_token: '',
        expires_at: new Date(Date.now() + 3_600_000).toISOString(),
        user_id: user.id,
        username: user.username,
        scope: scopes.join(' '),
      },
      null,
      2,
    ),
    { mode: 0o600 },
  )
  return configDir
}

async function runRealCLI(
  configDir: string,
  hubURL: string,
  cwd: string,
  args: string[],
  extraEnv: Record<string, string> = {},
): Promise<string> {
  const cleanEnv: Record<string, string> = {}
  for (const [k, v] of Object.entries(process.env)) {
    if (v !== undefined && !k.startsWith('LEAPMUX_'))
      cleanEnv[k] = v
  }
  const { stdout } = await execFileAsync(
    BINARY_PATH,
    ['--hub', hubURL, ...args],
    {
      cwd,
      env: {
        ...cleanEnv,
        ...extraEnv,
        LEAPMUX_CONTROL_CONFIG_DIR: configDir,
        LEAPMUX_CONTROL_NO_PROMPT: '1',
      },
      maxBuffer: 10 * 1024 * 1024,
    },
  )
  return stdout
}

async function main(): Promise<void> {
  const runDir = mkdtempSync(join(realpathSync('/tmp'), 'lm-cast-'))
  let suiteStop: (() => Promise<void>) | undefined
  let hubUrlForCleanup: string | undefined

  try {
    console.log(`Creating fixture Git repository in ${runDir} ...`)
    const repoDir = createPaymentsRepo(runDir)

    console.log('Starting real LeapMux Hub + Worker + mock model server ...')
    const suite = await startSuiteServer({
      binaryPath: BINARY_PATH,
      tmpDir: runDir,
      runRoot: runDir,
    })
    suiteStop = suite.stop
    const { state } = suite
    hubUrlForCleanup = state.hubUrl

    // Script deterministic replies: Claude (feat/api) completes first so
    // `leapmux agent wait "$A1" "$A2" --any --json` genuinely returns A1 first.
    await extendMockModelScenario(state.mockModelUrl, AMBIENT_SCENARIO_ID, {
      rules: [
        {
          name: 'cli-cast-health-endpoint',
          priority: 'high',
          when: { user: 'Implement the /health endpoint with tests.' },
          respond: {
            text: 'Added GET /health returning {"status":"ok","version":"1.4.2"} and 3 unit tests in internal/handler/health_test.go — all tests pass with `go test -race ./...`.',
            delayMs: 900,
          },
        },
        {
          name: 'cli-cast-health-subcommand',
          priority: 'high',
          when: { user: 'Add the health subcommand and JSON output.' },
          respond: {
            text: 'Implemented `payments health --json` subcommand with HTTP timeout handling and table/JSON formatters.',
            delayMs: 6500,
          },
        },
      ],
    })

    const wsId = await createWorkspaceViaAPI(state.hubUrl, state.adminToken, 'payments-api')
    const cliConfigDir = await mintCLIConfigInDir(state.hubUrl, state.adminToken, runDir)
    const baseEnv = {
      LEAPMUX_CONTROL_WORKSPACE_ID: wsId,
      LEAPMUX_CONTROL_WORKER_ID: state.workerId,
      LEAPMUX_CONTROL_WORKING_DIR: repoDir,
    }

    // Confirm binary responds cleanly before executing the recorded commands.
    await execFileAsync(BINARY_PATH, ['--version'])

    console.log('Running real leapmux CLI fleet sequence ...')
    const a1Raw = await runRealCLI(
      cliConfigDir,
      state.hubUrl,
      repoDir,
      [
        'agent',
        'open',
        '--workspace',
        wsId,
        '--dir',
        repoDir,
        '--provider',
        'claude',
        '--worktree',
        'feat/api',
        '--base',
        'main',
        '--permission-mode',
        'bypassPermissions',
        '-q',
      ],
      baseEnv,
    )
    const a1 = a1Raw.trim()
    if (!a1)
      throw new Error('Expected non-empty agent ID for A1')

    const a2Raw = await runRealCLI(
      cliConfigDir,
      state.hubUrl,
      repoDir,
      [
        'agent',
        'open',
        '--workspace',
        wsId,
        '--dir',
        repoDir,
        '--provider',
        'codex',
        '--worktree',
        'feat/cli',
        '--base',
        'main',
        '-q',
      ],
      baseEnv,
    )
    const a2 = a2Raw.trim()
    if (!a2)
      throw new Error('Expected non-empty agent ID for A2')

    // Wait briefly until both newly opened agents report status "running" so
    // `leapmux agent list` displays clean ready rows instead of "starting".
    const deadline = Date.now() + 15_000
    let listOut = ''
    while (Date.now() < deadline) {
      listOut = await runRealCLI(
        cliConfigDir,
        state.hubUrl,
        repoDir,
        [
          'agent',
          'list',
          '--workspace',
          wsId,
          '--color',
          'always',
        ],
        baseEnv,
      )
      if (!listOut.includes('starting'))
        break
      await new Promise(resolveDelay => setTimeout(resolveDelay, 250))
    }

    const send1Out = await runRealCLI(
      cliConfigDir,
      state.hubUrl,
      repoDir,
      [
        'agent',
        'send',
        '--workspace',
        wsId,
        a1,
        'Implement the /health endpoint with tests.',
        '--color',
        'always',
      ],
      baseEnv,
    )
    const send2Out = await runRealCLI(
      cliConfigDir,
      state.hubUrl,
      repoDir,
      [
        'agent',
        'send',
        '--workspace',
        wsId,
        a2,
        'Add the health subcommand and JSON output.',
        '--color',
        'always',
      ],
      baseEnv,
    )

    const waitJsonRaw = await runRealCLI(
      cliConfigDir,
      state.hubUrl,
      repoDir,
      [
        'agent',
        'wait',
        '--workspace',
        wsId,
        a1,
        a2,
        '--any',
        '--json',
      ],
      baseEnv,
    )
    const waitParsed = JSON.parse(waitJsonRaw.trim()) as Record<string, unknown>

    const messagesJsonRaw = await runRealCLI(
      cliConfigDir,
      state.hubUrl,
      repoDir,
      [
        'agent',
        'messages',
        '--workspace',
        wsId,
        a1,
        '--json',
      ],
      baseEnv,
    )
    const messagesParsed = JSON.parse(messagesJsonRaw.trim()) as {
      data?: { messages?: Array<{ source?: string, reply_text?: string }> }
    }
    const completedReply = (messagesParsed.data?.messages ?? [])
      .filter(m => m.source === 'agent')
      .map(m => m.reply_text?.trim() ?? '')
      .filter(Boolean)
      .join('\n')
    if (!completedReply)
      throw new Error('Expected non-empty agent reply_text from completed agent A1')

    const replacements: Array<readonly [string, string]> = [
      [repoDir, '~/src/payments-api'],
      [runDir, '~/src'],
    ]
    const clean = (s: string) => sanitizeCastText(s, replacements)

    // Assemble the asciicast v2 replay from the real CLI outputs captured above.
    const cast = new AsciicastWriter({
      width: 100,
      height: 23,
      title: 'LeapMux CLI — parallel worktree agents',
    })

    const nextPrompt = () => {
      cast.writeLine('')
      cast.writePrompt()
    }

    cast.writeBanner()
    cast.pause(550)

    // Group the initial comment and A1=/A2= assignments without blank lines between them.
    nextPrompt()
    cast.typeLine('# Open two agents on isolated git worktrees', { baseCharMs: 22, ansiPrefix: '\x1B[90m' })
    cast.writePrompt()
    cast.typeLine('A1=$(leapmux agent open --provider claude --worktree feat/api --base main -q)', { baseCharMs: 24 })
    cast.pause(340)
    cast.writePrompt()
    cast.typeLine('A2=$(leapmux agent open --provider codex  --worktree feat/cli --base main -q)', { baseCharMs: 24 })
    cast.pause(340)

    nextPrompt()
    cast.typeLine('leapmux agent list', { baseCharMs: 26 })
    cast.pause(240)
    cast.writeOutputLines(clean(listOut), 32)

    cast.pause(450)
    nextPrompt()
    cast.typeLine('leapmux agent send "$A1" "Implement the /health endpoint with tests."', { baseCharMs: 24 })
    cast.pause(240)
    cast.writeOutputLines(clean(send1Out), 30)

    nextPrompt()
    cast.typeLine('leapmux agent send "$A2" "Add the health subcommand and JSON output."', { baseCharMs: 24 })
    cast.pause(240)
    cast.writeOutputLines(clean(send2Out), 30)

    cast.pause(320)
    nextPrompt()
    cast.typeLine('leapmux agent wait "$A1" "$A2" --any --json', { baseCharMs: 24 })
    cast.pause(950)
    cast.writeOutputLines(clean(colorizeJsonAnsi(waitParsed)), 28)

    cast.pause(550)
    nextPrompt()
    cast.typeLine('leapmux agent messages "$A1" --json | jq -r \'.data.messages[] | select(.source=="agent") | .reply_text // empty\'', { baseCharMs: 22 })
    cast.pause(280)
    cast.writeOutputLines(clean(completedReply), 30)

    nextPrompt()
    cast.holdEnd(5000)

    mkdirSync(dirname(OUT_PATH), { recursive: true })
    writeFileSync(OUT_PATH, cast.toString(), 'utf8')
    console.log(`Wrote ${OUT_PATH} (A1=${a1}, A2=${a2})`)
  }
  finally {
    await finishCleanup([
      hubUrlForCleanup
        ? closeTestChannels(hubUrlForCleanup)
        : Promise.resolve(),
      suiteStop
        ? suiteStop()
        : Promise.resolve(),
    ])
    rmSync(runDir, { recursive: true, force: true })
  }
}

await main()
process.exit(0)
