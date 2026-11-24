/**
 * Deterministic documentation screenshot generator for https://leapmux.dev.
 *
 * Boots a real isolated LeapMux Hub + Worker + embedded SolidJS frontend
 * (`./leapmux`), registers a second online Worker (`gpu-devbox`), builds a
 * deterministic fixture Git repository with linked worktrees, scripts realistic
 * Claude Code / Codex / Antigravity sessions against the local mock model
 * server (zero network calls, zero API keys), drives a real xterm.js terminal
 * and Git diff viewer, and captures 12 Retina (2x) PNGs (6 views x light/dark)
 * into `site/static/images/docs/`.
 *
 * Run explicitly by developers when the UI or fixtures change:
 *   task generate-site-screenshots
 */

import type { Locator, Page } from '@playwright/test'
import type { ChildProcess } from 'node:child_process'
import { execFile, execFileSync, spawn } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { mkdirSync, mkdtempSync, realpathSync, rmSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'
import { AgentProvider } from '../src/generated/proto/leapmux/v1/agent_pb'
import { TabFileViewMode, TabInsertion } from '../src/generated/proto/leapmux/v1/tab_creation_pb'
import { WorkerTerminalService } from '../src/generated/proto/leapmux/v1/terminal_pb'
import { WorkerPrivateService } from '../src/generated/proto/leapmux/v1/worker_private_pb'
import { agentOpenOptions } from '../tests/e2e/agentSettings'
import {
  closeTestChannels,
  createWorkspaceViaAPI,
  getTestChannel,
  listWorkersViaAPI,
  mintRegistrationKeyViaAPI,
  openAgentViaAPI,
  waitForNewOnlineWorkerViaAPI,
} from '../tests/e2e/helpers/api'
import { finishCleanup } from '../tests/e2e/helpers/cleanup'
import { E2E_BROWSER_HOST } from '../tests/e2e/helpers/hubTransport'
import { extendMockModelScenario } from '../tests/e2e/helpers/mockModelScenario'
import { AMBIENT_SCENARIO_ID } from '../tests/e2e/helpers/mockModelScript'
import { stopProcess } from '../tests/e2e/helpers/process'
import { trackProcess } from '../tests/e2e/helpers/processRegistry'
import { bashToolCall, editToolCall, readToolCall } from '../tests/e2e/helpers/providerToolCalls'
import { deliberateWorkingDir } from '../tests/e2e/helpers/providerWorkingDir'
import { hubSpawnEnv } from '../tests/e2e/helpers/server'
import { startSuiteServer } from '../tests/e2e/helpers/suiteServer'
import { waitForAgentStartupViaAPI } from '../tests/e2e/helpers/workerTabs'
import { initGitRepo } from '../tests/e2e/helpers/worktree'

const execFileAsync = promisify(execFile)
const require = createRequire(import.meta.url)
const { chromium } = require('playwright-core') as typeof import('@playwright/test')

const SCRIPT_DIR = dirname(fileURLToPath(import.meta.url))
const ROOT = resolve(SCRIPT_DIR, '../..')
const BINARY_PATH = resolve(ROOT, process.platform === 'win32'
  ? 'leapmux.exe'
  : 'leapmux')
const OUT_DIR = resolve(ROOT, 'site/static/images/docs')

const VIEWPORT = { width: 1440, height: 900 }

interface ClipRect {
  x: number
  y: number
  width: number
  height: number
}

interface FixtureRepos {
  paymentsMain: string
  wtRetryJitter: string
  wtWebhookSigs: string
  wtTokenRace: string
  telemetryRepo: string
  backoffPath: string
}

const BACKOFF_BEFORE = `package retry

import "time"

const (
\tbaseBackoff = 150 * time.Millisecond
\tmaxBackoff  = 30 * time.Second
)

// CalculateBackoff returns the delay before retrying an HTTP call.
func CalculateBackoff(attempt int) time.Duration {
\tif attempt <= 0 {
\t\treturn baseBackoff
\t}
\tdelay := baseBackoff << min(attempt, 10)
\tif delay > maxBackoff {
\t\tdelay = maxBackoff
\t}
\treturn delay
}
`

const BACKOFF_OLD_SNIPPET = `import "time"

const (
\tbaseBackoff = 150 * time.Millisecond
\tmaxBackoff  = 30 * time.Second
)

// CalculateBackoff returns the delay before retrying an HTTP call.
func CalculateBackoff(attempt int) time.Duration {
\tif attempt <= 0 {
\t\treturn baseBackoff
\t}
\tdelay := baseBackoff << min(attempt, 10)
\tif delay > maxBackoff {
\t\tdelay = maxBackoff
\t}
\treturn delay
}`

const BACKOFF_NEW_SNIPPET = `import (
\t"math/rand/v2"
\t"time"
)

const (
\tbaseBackoff = 150 * time.Millisecond
\tmaxBackoff  = 30 * time.Second
)

// CalculateBackoff returns a full-jitter delay before retrying an HTTP call.
func CalculateBackoff(attempt int) time.Duration {
\tif attempt <= 0 {
\t\treturn baseBackoff
\t}
\tceiling := min(baseBackoff<<min(attempt, 10), maxBackoff)
\treturn rand.N(ceiling + 1)
}`

const BACKOFF_TEST = `package retry

import (
\t"testing"
\t"time"
)

func TestCalculateBackoffRespectsCap(t *testing.T) {
\tfor attempt := 0; attempt < 20; attempt++ {
\t\td := CalculateBackoff(attempt)
\t\tif d < 0 || d > maxBackoff {
\t\t\tt.Fatalf("attempt %d: backoff %v outside [0, %v]", attempt, d, maxBackoff)
\t\t}
\t}
}

func TestCalculateBackoffZeroAttempt(t *testing.T) {
\tif got := CalculateBackoff(0); got != baseBackoff {
\t\tt.Fatalf("attempt 0: got %v, want %v", got, baseBackoff)
\t}
}

func TestCalculateBackoffSpreadsAttempts(t *testing.T) {
\tseen := make(map[time.Duration]struct{})
\tfor range 32 {
\t\tseen[CalculateBackoff(4)] = struct{}{}
\t}
\tif len(seen) < 2 {
\t\tt.Fatalf("expected jittered samples, got %d distinct values", len(seen))
\t}
}
`

const WEBHOOK_INITIAL = `package handler

import "net/http"

func HandleWebhook(w http.ResponseWriter, r *http.Request) {
\tw.WriteHeader(http.StatusAccepted)
}
`

const WEBHOOK_RETRY_MOD = `package handler

import (
\t"net/http"
\t"strconv"
)

func HandleWebhook(w http.ResponseWriter, r *http.Request) {
\tif attempt, err := strconv.Atoi(r.Header.Get("X-Retry-Attempt")); err == nil && attempt > 0 {
\t\tw.Header().Set("X-Backoff-Policy", "full-jitter")
\t}
\tw.WriteHeader(http.StatusAccepted)
}
`

const WEBHOOK_HMAC_MOD = `package handler

import (
\t"crypto/hmac"
\t"crypto/sha256"
\t"encoding/hex"
\t"net/http"
)

func VerifySignature(secret, payload []byte, header string) bool {
\tmac := hmac.New(sha256.New, secret)
\tmac.Write(payload)
\texpected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
\treturn hmac.Equal([]byte(expected), []byte(header))
}

func HandleWebhook(w http.ResponseWriter, r *http.Request) {
\tw.WriteHeader(http.StatusAccepted)
}
`

const TOKEN_INITIAL = `package auth

import "time"

type TokenCache struct {
\tvalue     string
\texpiresAt time.Time
}

func (c *TokenCache) Valid(now time.Time) bool {
\treturn c.value != "" && now.Before(c.expiresAt)
}
`

const TOKEN_RACE_MOD = `package auth

import (
\t"sync"
\t"time"
)

type TokenCache struct {
\tmu        sync.RWMutex
\tvalue     string
\texpiresAt time.Time
}

func (c *TokenCache) Valid(now time.Time) bool {
\tc.mu.RLock()
\tdefer c.mu.RUnlock()
\treturn c.value != "" && now.Before(c.expiresAt)
}
`

function writeFile(filePath: string, content: string): void {
  mkdirSync(dirname(filePath), { recursive: true })
  writeFileSync(filePath, content, 'utf8')
}

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

function createFixtureRepos(runDir: string): FixtureRepos {
  const paymentsMain = join(runDir, 'repos', 'payments-api')
  mkdirSync(paymentsMain, { recursive: true })
  initGitRepo(paymentsMain)
  git(paymentsMain, 'remote', 'add', 'origin', 'https://github.com/acme/payments-api.git')

  writeFile(join(paymentsMain, 'go.mod'), 'module acme.dev/payments\n\ngo 1.24\n')
  writeFile(join(paymentsMain, 'README.md'), '# payments-api\n\nCheckout retry and webhook delivery service.\n')
  writeFile(join(paymentsMain, 'internal/retry/backoff.go'), BACKOFF_BEFORE)
  writeFile(join(paymentsMain, 'internal/handler/webhook.go'), WEBHOOK_INITIAL)
  writeFile(join(paymentsMain, 'internal/auth/token.go'), TOKEN_INITIAL)
  git(paymentsMain, 'add', '.')
  git(paymentsMain, 'commit', '-m', 'feat: initial checkout retry and webhook service')

  // Create linked worktrees whose leaf directory basename is `payments-api` so
  // the File Browser root node displays `payments-api` cleanly.
  const wtRetryJitter = join(runDir, 'wt', 'retry-jitter', 'payments-api')
  const wtWebhookSigs = join(runDir, 'wt', 'webhook-signatures', 'payments-api')
  const wtTokenRace = join(runDir, 'wt', 'token-refresh-race', 'payments-api')
  mkdirSync(dirname(wtRetryJitter), { recursive: true })
  mkdirSync(dirname(wtWebhookSigs), { recursive: true })
  mkdirSync(dirname(wtTokenRace), { recursive: true })

  git(paymentsMain, 'worktree', 'add', '-b', 'feat/retry-jitter', wtRetryJitter)
  git(paymentsMain, 'worktree', 'add', '-b', 'feat/webhook-signatures', wtWebhookSigs)
  git(paymentsMain, 'worktree', 'add', '-b', 'fix/token-refresh-race', wtTokenRace)

  // Seed realistic working-tree diffs so File Browser and sidebar diff badges
  // display authentic modified/untracked files.
  writeFile(join(wtRetryJitter, 'internal/handler/webhook.go'), WEBHOOK_RETRY_MOD)
  writeFile(join(wtRetryJitter, 'internal/retry/backoff_test.go'), BACKOFF_TEST)
  writeFile(join(wtWebhookSigs, 'internal/handler/webhook.go'), WEBHOOK_HMAC_MOD)
  writeFile(join(wtTokenRace, 'internal/auth/token.go'), TOKEN_RACE_MOD)

  // Secondary repository for a collapsed workspace in the sidebar.
  const telemetryRepo = join(runDir, 'repos', 'telemetry-store')
  mkdirSync(telemetryRepo, { recursive: true })
  initGitRepo(telemetryRepo)
  git(telemetryRepo, 'remote', 'add', 'origin', 'https://github.com/acme/telemetry-store.git')
  writeFile(join(telemetryRepo, 'go.mod'), 'module github.com/acme/telemetry-store\n\ngo 1.24\n')
  writeFile(join(telemetryRepo, 'store/wal.go'), 'package store\n\nconst JournalMode = "DELETE"\n')
  git(telemetryRepo, 'add', '.')
  git(telemetryRepo, 'commit', '-m', 'feat: initial telemetry store')
  git(telemetryRepo, 'checkout', '-b', 'perf/sqlite-wal')
  writeFile(
    join(telemetryRepo, 'store/wal.go'),
    'package store\n\nconst (\n\tJournalMode     = "WAL"\n\tBusyTimeoutMS   = 5000\n\tSynchronousMode = "NORMAL"\n)\n',
  )

  return {
    paymentsMain,
    wtRetryJitter,
    wtWebhookSigs,
    wtTokenRace,
    telemetryRepo,
    backoffPath: join(wtRetryJitter, 'internal/retry/backoff.go'),
  }
}

async function waitFor<T>(
  description: string,
  check: () => Promise<T | null | undefined | false>,
  timeoutMs = 30_000,
  intervalMs = 150,
): Promise<T> {
  const deadline = Date.now() + timeoutMs
  let lastError: unknown
  while (Date.now() < deadline) {
    try {
      const value = await check()
      if (value)
        return value
    }
    catch (error) {
      lastError = error
    }
    await new Promise(resolveDelay => setTimeout(resolveDelay, intervalMs))
  }
  const suffix = lastError instanceof Error
    ? ` (${lastError.message})`
    : ''
  throw new Error(`Timed out waiting for ${description}${suffix}`)
}

async function startSecondWorker(
  runDir: string,
  hubUrl: string,
  adminToken: string,
  agentEnv: Record<string, string>,
): Promise<{ proc: ChildProcess, workerId: string }> {
  const dataDir = join(runDir, 'worker-gpu-devbox')
  mkdirSync(dataDir, { recursive: true })
  const before = new Set((await listWorkersViaAPI(hubUrl, adminToken)).map(w => w.id))
  const regKey = await mintRegistrationKeyViaAPI(hubUrl, adminToken)
  const env = hubSpawnEnv({ ...agentEnv, LEAPMUX_WORKER_NAME: 'gpu-devbox' })

  await execFileAsync(BINARY_PATH, [
    'daemon',
    'worker',
    'register',
    '--hub',
    hubUrl,
    '--registration-key',
    regKey,
    '--data-dir',
    dataDir,
  ], { env })

  const proc = spawn(BINARY_PATH, [
    'daemon',
    'worker',
    'run',
    '--data-dir',
    dataDir,
  ], {
    stdio: ['ignore', 'ignore', 'ignore'],
    env,
  })
  trackProcess(runDir, proc)

  const workerId = await waitForNewOnlineWorkerViaAPI(hubUrl, adminToken, before, 30_000)
  return { proc, workerId }
}

function clampClip(rect: ClipRect): ClipRect {
  const x = Math.max(0, Math.round(rect.x))
  const y = Math.max(0, Math.round(rect.y))
  const right = Math.min(VIEWPORT.width, Math.round(rect.x + rect.width))
  const bottom = Math.min(VIEWPORT.height, Math.round(rect.y + rect.height))
  return {
    x,
    y,
    width: Math.max(1, right - x),
    height: Math.max(1, bottom - y),
  }
}

async function requireBox(locator: Locator, label: string): Promise<ClipRect> {
  const box = await locator.boundingBox()
  if (!box || box.width <= 0 || box.height <= 0)
    throw new Error(`Could not measure bounding box for ${label}`)
  return box
}

async function typeComposerAndSend(page: Page, text: string): Promise<void> {
  const editor = page.locator('[data-testid="composer-editor"]:visible .ProseMirror[contenteditable="true"]').first()
  await editor.waitFor({ state: 'visible', timeout: 20_000 })
  await editor.locator(':scope > :first-child').click()
  await page.keyboard.insertText(text)
  const mod = process.platform === 'darwin'
    ? 'Meta'
    : 'Control'
  await page.keyboard.press(`${mod}+Enter`)
  await waitFor('composer cleared after send', async () => {
    const remaining = await editor.textContent().catch(() => '')
    return (remaining ?? '').trim() === ''
  }, 15_000)
}

async function waitForControlAllowButton(page: Page, timeoutMs = 30_000): Promise<Locator> {
  const banner = page.locator('[data-testid="control-banner"]:visible').first()
  await banner.waitFor({ state: 'visible', timeout: timeoutMs })
  const allowBtn = page.getByTestId('control-allow-btn').filter({ visible: true }).first()
  await allowBtn.waitFor({ state: 'visible', timeout: timeoutMs })
  return allowBtn
}

async function getActiveTerminalText(page: Page): Promise<string> {
  return page.evaluate(() => {
    const win = window as unknown as { __getActiveTerminalText?: () => string }
    if (typeof win.__getActiveTerminalText === 'function')
      return win.__getActiveTerminalText()
    return document.querySelector('[data-terminal-id][data-active="true"] .xterm-rows')?.textContent ?? ''
  })
}

async function sendTerminalLine(page: Page, tile: Locator, command: string): Promise<void> {
  await tile.click({
    position: { x: 40, y: 80 },
  })
  const textarea = tile.locator('.xterm-helper-textarea').first()
  await textarea.focus()
  await page.keyboard.type(command, { delay: 10 })
  await page.keyboard.press('Enter')
}

async function captureCodingAgentChat(page: Page, theme: 'light' | 'dark'): Promise<ClipRect> {
  await page.emulateMedia({ colorScheme: theme })
  await waitFor(`html[data-theme="${theme}"]`, async () => {
    return (await page.locator('html').getAttribute('data-theme')) === theme
  }, 10_000)
  await page.waitForTimeout(500)
  await page.evaluate(() => document.fonts.ready)

  const resizeHandles = page.locator('[data-testid="resize-handle"]')
  const leftHandle = await requireBox(resizeHandles.nth(0), 'left resize handle')
  const rightHandle = await requireBox(resizeHandles.nth(1), 'right resize handle')
  const clip = clampClip({
    x: leftHandle.x - 24,
    y: 0,
    width: (rightHandle.x + rightHandle.width + 24) - (leftHandle.x - 24),
    height: VIEWPORT.height,
  })
  const outPath = join(OUT_DIR, `coding-agent-chat-${theme}.png`)
  await page.screenshot({ path: outPath, clip, animations: 'disabled' })
  console.log(`  wrote coding-agent-chat-${theme}.png (${clip.width}x${clip.height} @2x)`)
  return clip
}

async function captureWorkspaceViews(page: Page, theme: 'light' | 'dark'): Promise<Record<string, ClipRect>> {
  await page.emulateMedia({ colorScheme: theme })
  await waitFor(`html[data-theme="${theme}"]`, async () => {
    const current = await page.locator('html').getAttribute('data-theme')
    return current === theme
  }, 10_000)

  // Allow CSS custom properties, Shiki code highlights, and xterm.js DOM
  // palettes to repaint cleanly after the theme transition.
  await page.waitForTimeout(600)
  await page.evaluate(() => document.fonts.ready)

  const tiles = page.locator('[data-testid="tile"]')
  const tile1 = await requireBox(tiles.nth(1), 'tile 1 (diff viewer)')
  const tile2 = await requireBox(tiles.nth(2), 'tile 2 (terminal)')
  const resizeHandles = page.locator('[data-testid="resize-handle"]')
  const leftHandle = await requireBox(resizeHandles.nth(0), 'left resize handle')
  const rightHandle = await requireBox(resizeHandles.nth(1), 'right resize handle')

  const clips: Record<string, ClipRect> = {
    'hero-workspace': {
      x: 0,
      y: 0,
      width: VIEWPORT.width,
      height: VIEWPORT.height,
    },
    'sidebar-workspaces': clampClip({
      x: 0,
      y: 0,
      width: leftHandle.x + leftHandle.width + 150,
      height: VIEWPORT.height,
    }),
    'terminal-pane': clampClip({
      x: tile2.x - 24,
      y: tile2.y - 28,
      width: tile2.width + 48,
      height: tile2.height + 52,
    }),
    'tabs-tiling-layout': clampClip({
      x: leftHandle.x - 24,
      y: 0,
      width: (rightHandle.x + rightHandle.width + 24) - (leftHandle.x - 24),
      height: VIEWPORT.height,
    }),
    'file-browser-diff': clampClip({
      x: tile1.x - 24,
      y: 0,
      width: VIEWPORT.width - (tile1.x - 24),
      height: tile1.y + tile1.height + 72,
    }),
  }

  for (const [name, clip] of Object.entries(clips)) {
    const outPath = join(OUT_DIR, `${name}-${theme}.png`)
    await page.screenshot({
      path: outPath,
      clip,
      animations: 'disabled',
    })
    console.log(`  wrote ${name}-${theme}.png (${clip.width}x${clip.height} @2x)`)
  }
  return clips
}

async function main(): Promise<void> {
  mkdirSync(OUT_DIR, { recursive: true })
  // Native agent CLIs read instruction files from ancestor directories and
  // macOS caps Unix domain socket paths at 104 bytes, so keep the ephemeral
  // run root under /tmp (matching frontend/scripts/run-e2e.ts) and remove it
  // unconditionally in finally.
  const runDir = mkdtempSync(join(realpathSync('/tmp'), 'lm-docs-'))
  let secondWorker: ChildProcess | undefined
  let suiteStop: (() => Promise<void>) | undefined
  let hubUrlForCleanup: string | undefined

  try {
    console.log(`Creating deterministic fixture repos in ${runDir} ...`)
    const repos = createFixtureRepos(runDir)

    console.log('Starting real LeapMux Hub + Worker + mock model server ...')
    const suite = await startSuiteServer({
      binaryPath: BINARY_PATH,
      tmpDir: runDir,
      runRoot: runDir,
    })
    suiteStop = suite.stop
    const { state } = suite
    hubUrlForCleanup = state.hubUrl

    console.log('Registering second online Worker (gpu-devbox) ...')
    const gpuWorker = await startSecondWorker(runDir, state.hubUrl, state.adminToken, state.agentEnv)
    secondWorker = gpuWorker.proc

    // Extend the ambient scenario so clean human prompts (no E2E markers) get
    // realistic, deterministic tool calls and responses.
    await extendMockModelScenario(state.mockModelUrl, AMBIENT_SCENARIO_ID, {
      rules: [
        {
          name: 'antigravity-title',
          priority: 'high',
          when: { system: 'conversation title generator' },
          respond: { text: 'Audit token refresh' },
        },
      ],
      steps: [
        {
          text: 'I will inspect `internal/retry/backoff.go` first to see how retry delays are calculated.',
          toolCalls: [
            readToolCall(AgentProvider.CLAUDE_CODE, 'call_read_backoff', repos.backoffPath),
          ],
          usage: {
            inputTokens: 34200,
            outputTokens: 180,
            cacheReadTokens: 24000,
            contextWindow: 200000,
          },
        },
        {
          text: '`CalculateBackoff` currently returns an unjittered exponential delay, which lets retrying clients synchronize after an outage. I will update it to apply full jitter via `math/rand/v2` while preserving the `maxBackoff` cap.',
          toolCalls: [
            editToolCall(AgentProvider.CLAUDE_CODE, 'call_edit_backoff', {
              path: repos.backoffPath,
              before: BACKOFF_OLD_SNIPPET,
              after: BACKOFF_NEW_SNIPPET,
            }),
          ],
          usage: {
            inputTokens: 42800,
            outputTokens: 520,
            cacheReadTokens: 31000,
            contextWindow: 200000,
          },
        },
        {
          text: 'The backoff implementation is updated. Next, I will run the retry package tests with the race detector enabled.',
          toolCalls: [
            {
              ...bashToolCall(AgentProvider.CLAUDE_CODE, 'call_test_retry', 'go test -race -v ./internal/retry/...'),
              arguments: {
                command: 'go test -race -v ./internal/retry/...',
                description: 'Run retry package tests with race detector',
              },
            },
          ],
          usage: {
            inputTokens: 48200,
            outputTokens: 290,
            cacheReadTokens: 36000,
            contextWindow: 200000,
          },
        },
        {
          text: 'All retry package tests passed with `-race`.',
          usage: {
            inputTokens: 51400,
            outputTokens: 140,
            cacheReadTokens: 39000,
            contextWindow: 200000,
          },
        },
      ],
    })

    console.log('Creating realistic workspaces and opening Claude Code, Codex, and Antigravity agents ...')
    const server = { hubUrl: state.hubUrl, adminToken: state.adminToken, workerId: state.workerId }

    // Create secondary workspaces first so the active workspace sits at the top.
    const wdReason = 'documentation screenshot fixture repository'
    const wsArchived = await createWorkspaceViaAPI(state.hubUrl, state.adminToken, 'v0.9 telemetry cleanup')
    await openAgentViaAPI(server, wsArchived, deliberateWorkingDir(repos.telemetryRepo, wdReason), {
      ...agentOpenOptions(AgentProvider.CLAUDE_CODE, {
        optionValues: { permissionMode: 'default' },
      }),
      title: 'Drop legacy counters',
    })

    const wsOAuth = await createWorkspaceViaAPI(state.hubUrl, state.adminToken, 'OAuth PKCE callback audit')
    await openAgentViaAPI(server, wsOAuth, deliberateWorkingDir(repos.wtTokenRace, wdReason), {
      ...agentOpenOptions(AgentProvider.CODEX),
      title: 'Check state nonce',
    })

    const wsWal = await createWorkspaceViaAPI(state.hubUrl, state.adminToken, 'SQLite WAL migration')
    await openAgentViaAPI(server, wsWal, deliberateWorkingDir(repos.telemetryRepo, wdReason), {
      ...agentOpenOptions(AgentProvider.CLAUDE_CODE, {
        optionValues: { permissionMode: 'default' },
      }),
      title: 'Enable WAL pragmas',
    })

    const wsMain = await createWorkspaceViaAPI(state.hubUrl, state.adminToken, 'Checkout retry backoff')
    const claudeId = await openAgentViaAPI(server, wsMain, deliberateWorkingDir(repos.wtRetryJitter, wdReason), {
      ...agentOpenOptions(AgentProvider.CLAUDE_CODE, {
        optionValues: { permissionMode: 'default' },
      }),
      title: 'Retry jitter',
    })
    await openAgentViaAPI(server, wsMain, deliberateWorkingDir(repos.wtTokenRace, wdReason), {
      ...agentOpenOptions(AgentProvider.ANTIGRAVITY_CLI),
      title: 'Token refresh',
    })
    await openAgentViaAPI(server, wsMain, deliberateWorkingDir(repos.wtWebhookSigs, wdReason), {
      ...agentOpenOptions(AgentProvider.CODEX),
      title: 'Webhook HMAC',
    })

    await waitForAgentStartupViaAPI(state.hubUrl, state.adminToken, state.workerId, wsMain, 3, 45_000)

    console.log('Launching headless Chromium against real LeapMux frontend ...')
    // Disable WebGL so xterm.js uses its native DOM renderer instead of
    // SwiftShader WebGL, which double-scales glyphs under deviceScaleFactor: 2.
    const browser = await chromium.launch({
      args: ['--disable-webgl', '--disable-webgl2'],
    })
    try {
      const context = await browser.newContext({
        baseURL: state.hubUrl,
        viewport: VIEWPORT,
        deviceScaleFactor: 2,
        colorScheme: 'light',
      })
      const [cookieName, ...cookieRest] = state.adminToken.split('=')
      if (!cookieName)
        throw new Error(`Invalid session cookie: ${state.adminToken}`)
      await context.addCookies([
        {
          name: cookieName,
          value: cookieRest.join('='),
          domain: E2E_BROWSER_HOST,
          path: '/',
          httpOnly: true,
        },
      ])

      const page = await context.newPage()
      await page.goto('/', { waitUntil: 'domcontentloaded' })
      const mainRow = page.locator(`[data-testid="workspace-item-${wsMain}"]:visible`).first()
      await mainRow.waitFor({ state: 'visible', timeout: 30_000 })
      if (await mainRow.getAttribute('data-active') !== 'true')
        await mainRow.getByTestId('workspace-title').click()
      await page.locator('[data-testid="tile"]').first().waitFor({ state: 'visible', timeout: 30_000 })

      // Archive the fourth workspace via UI so the Archived section appears naturally.
      const archivedRow = page.locator(`[data-testid="workspace-item-${wsArchived}"]:visible`).first()
      await archivedRow.waitFor({ state: 'visible', timeout: 15_000 })
      await archivedRow.hover()
      await archivedRow.getByTestId('workspace-row-menu-trigger').click()
      await archivedRow.getByRole('menuitem', { name: 'Archive', exact: true }).click()
      const archiveDialog = page.getByRole('dialog', { name: 'Archive workspace' })
      await archiveDialog.waitFor({ state: 'visible', timeout: 10_000 })
      await archiveDialog.getByRole('button', { name: 'Archive', exact: true }).click()
      await archiveDialog.waitFor({ state: 'hidden', timeout: 10_000 })

      // Ensure Workers section is expanded in the left sidebar.
      const workersHeader = page.locator('[data-testid="section-header-workers"]:visible').first()
      await workersHeader.waitFor({ state: 'visible', timeout: 15_000 })
      const workersClosed = await workersHeader.evaluate(el => el.hasAttribute('data-closed'))
      if (workersClosed)
        await workersHeader.locator('> [role="button"]').click()
      await waitFor('gpu-devbox visible in Workers sidebar', async () => {
        return page.getByText('gpu-devbox').first().isVisible()
      }, 15_000)

      // Ensure the active workspace tree is expanded in the left sidebar.
      const mainChildren = page.locator(`[data-testid="workspace-children-${wsMain}"]:visible`).first()
      if (!await mainChildren.isVisible().catch(() => false)) {
        await mainRow.locator(`[data-testid="workspace-chevron-${wsMain}"]`).click()
        await mainChildren.waitFor({ state: 'visible', timeout: 10_000 })
      }

      // Select the Claude Code tab explicitly.
      const claudeTab = page.locator(`[data-testid="tab"][data-tab-id="${claudeId}"]:visible`).first()
      await claudeTab.click()

      console.log('Driving live Claude Code turn (Read -> Edit diff -> Bash permission prompt + queued steer) ...')
      await typeComposerAndSend(
        page,
        'Add full jitter to CalculateBackoff in internal/retry/backoff.go so retrying clients do not synchronize retries, then run the retry package tests.',
      )

      // Approve the Edit tool call so Claude writes backoff.go and renders the real Edit diff card.
      const editAllowBtn = await waitForControlAllowButton(page, 45_000)
      await editAllowBtn.click()

      // Wait for the next permission prompt (the Bash `go test` call) and capture
      // coding-agent-chat while the agent tile spans the full center width so the
      // Edit diff card, Permission Required banner, and all 3 tabs fit without clipping.
      await waitFor('Bash permission prompt visible', async () => {
        const banner = page.locator('[data-testid="control-banner"]:visible').first()
        if (!await banner.isVisible().catch(() => false))
          return false
        const text = await banner.textContent()
        return Boolean(text && text.includes('go test'))
      }, 45_000)

      console.log('Capturing coding-agent-chat screenshots (light & dark) ...')
      const chatClip = await captureCodingAgentChat(page, 'light')
      await captureCodingAgentChat(page, 'dark')
      await page.emulateMedia({ colorScheme: 'light' })

      // Approve the Bash command so the turn completes cleanly and the shared
      // composer collapses back to its compact height for the 3-pane layout views.
      const bashAllowBtn = await waitForControlAllowButton(page, 15_000)
      await bashAllowBtn.click()
      await waitFor('Claude turn completed after Bash tests', async () => {
        const text = await page.locator('[data-testid="tile"]').first().textContent()
        return Boolean(text && text.includes('All retry package tests passed'))
      }, 45_000)

      const waitForLayoutSaved = () => page.evaluate(() => new Promise<void>((resolveSaved) => {
        const timer = setTimeout(() => {
          window.removeEventListener('leapmux:layout-saved', onSaved)
          resolveSaved()
        }, 3000)
        function onSaved() {
          clearTimeout(timer)
          window.removeEventListener('leapmux:layout-saved', onSaved)
          resolveSaved()
        }
        window.addEventListener('leapmux:layout-saved', onSaved)
      }))

      console.log('Building 3-pane tiling layout (Agent | File Diff / Terminal) ...')
      // 1. Split vertically (left / right) -> creates Tile 1 on the right while
      // keeping the agent chat tile full height on the left.
      const split1Saved = waitForLayoutSaved()
      await page.locator('[data-testid="split-vertical"]').first().click()
      await waitFor('2 tiles visible', async () => (await page.locator('[data-testid="tile"]').count()) === 2, 10_000)
      await split1Saved

      const tile1 = page.locator('[data-testid="tile"]').nth(1)
      const tile1Id = await tile1.getAttribute('data-tile-id')
      if (!tile1Id)
        throw new Error('Tile 1 has no data-tile-id attribute')

      // Open backoff.go directly into Tile 1 in unified diff mode via WorkerPrivateService.
      const channel = await getTestChannel(state.hubUrl, state.adminToken)
      await waitFor('backoff.go tab registered in Tile 1', async () => {
        await channel.callWorker(
          state.workerId,
          WorkerPrivateService.method.registerTabPayload,
          {
            payload: {
              workingDir: repos.wtRetryJitter,
              kind: {
                case: 'file',
                value: { filePath: repos.backoffPath },
              },
            },
            creation: {
              tabId: randomUUID(),
              placement: {
                workspaceId: wsMain,
                destination: { case: 'tileId', value: tile1Id },
                insertion: TabInsertion.LAST,
              },
              presentation: {
                fileViewMode: TabFileViewMode.UNIFIED_DIFF,
                fileDiffBase: 'head-vs-working',
              },
            },
          },
        )
        return true
      }, 10_000, 200)
      const diffToolbar = tile1.locator('[data-testid="diff-mode-toolbar"]:visible').first()
      await diffToolbar.waitFor({ state: 'visible', timeout: 20_000 })
      // Switch explicitly to Unified diff so both removed and added lines show.
      await diffToolbar.getByText('Unified', { exact: true }).click()
      await waitFor('Unified diff lines rendered', async () => {
        const text = await tile1.textContent()
        return Boolean(text && text.includes('delay := baseBackoff') && text.includes('rand.N(ceiling + 1)'))
      }, 15_000)

      // 2. Split Tile 1 horizontally (top / bottom) -> creates Tile 2 at bottom-right.
      const split2Saved = waitForLayoutSaved()
      await page.locator('[data-testid="tile"]').nth(1).locator('[data-testid="split-horizontal"]').click()
      await waitFor('3 tiles visible', async () => (await page.locator('[data-testid="tile"]').count()) === 3, 10_000)
      await split2Saved

      const tile2 = page.locator('[data-testid="tile"]').nth(2)
      const tile2Id = await tile2.getAttribute('data-tile-id')
      if (!tile2Id)
        throw new Error('Tile 2 has no data-tile-id attribute')

      // 3. Open a real PTY terminal in Tile 2 via WorkerTerminalService.
      await waitFor('PTY terminal opened in Tile 2', async () => {
        await channel.callWorker(
          state.workerId,
          WorkerTerminalService.method.openTerminal,
          {
            workingDir: repos.wtRetryJitter,
            title: 'zsh',
            target: {
              case: 'creation',
              value: {
                tabId: randomUUID(),
                placement: {
                  workspaceId: wsMain,
                  destination: { case: 'tileId', value: tile2Id },
                  insertion: TabInsertion.LAST,
                },
              },
            },
          },
        )
        return true
      }, 10_000, 200)

      await tile2.locator('.xterm').waitFor({ state: 'visible', timeout: 20_000 })
      await waitFor('terminal startup overlay gone', async () => {
        return (await tile2.locator('[data-testid="terminal-startup-overlay"]').count()) === 0
      }, 20_000)

      // Wait until the real shell accepts input, then print clean colored output
      // from real git + go test runs without leaking temporary paths or wrapping.
      await waitFor('shell ready', async () => {
        await sendTerminalLine(page, tile2, 'echo LM_SHELL_READY')
        const txt = await getActiveTerminalText(page)
        return txt.includes('LM_SHELL_READY')
      }, 20_000, 400)

      const termScript = [
        `export PS1=$'\\e[36mpayments-api\\e[0m \\e[35m(retry-jitter)\\e[0m \\e[32m❯\\e[0m '`,
        'clear',
        `printf '\\e[36mpayments-api\\e[0m \\e[35m(retry-jitter)\\e[0m \\e[32m❯\\e[0m git status -sb\\n'`,
        'git status -sb',
        `printf '\\n\\e[36mpayments-api\\e[0m \\e[35m(retry-jitter)\\e[0m \\e[32m❯\\e[0m go test -v ./internal/retry\\n'`,
        'go test -v ./internal/retry',
      ].join(' && ')
      await sendTerminalLine(page, tile2, termScript)
      await waitFor('go test output rendered in xterm', async () => {
        const txt = await getActiveTerminalText(page)
        return txt.includes('PASS') && txt.includes('acme.dev/payments/internal/retry')
      }, 30_000, 300)

      // Ensure Tile 1 (backoff.go) is in Unified diff mode after split-horizontal
      // remounted it so both hero-workspace and file-browser-diff show the live diff.
      const tile1AfterSplit = page.locator('[data-testid="tile"]').nth(1)
      const diffToolbarAfterSplit = tile1AfterSplit.locator('[data-testid="diff-mode-toolbar"]:visible').first()
      await diffToolbarAfterSplit.waitFor({ state: 'visible', timeout: 15_000 })
      await diffToolbarAfterSplit.getByText('Unified', { exact: true }).click()
      await waitFor('Unified diff lines rendered after split', async () => {
        const text = await tile1AfterSplit.textContent()
        return Boolean(text && text.includes('delay := baseBackoff') && text.includes('rand.N(ceiling + 1)'))
      }, 15_000)

      // Focus Tile 0 (Claude Code) and select the Changed git filter in the
      // right File Browser so modified & untracked files show beside the diff.
      await page.locator('[data-testid="tile"]').nth(0).click({
        position: { x: 20, y: 12 },
      })
      const changedTab = page.locator('[data-testid="files-filter-changed"]:visible').first()
      await changedTab.waitFor({ state: 'visible', timeout: 15_000 })
      await changedTab.click()
      await waitFor('Changed filter tab selected', async () => {
        return (await changedTab.getAttribute('aria-selected')) === 'true'
      }, 10_000)
      for (const dirName of ['internal', 'handler', 'retry']) {
        const dirRow = page.locator('[data-testid="tree-row"]:visible')
          .filter({
            has: page.getByTestId('tree-row-name').getByText(dirName, { exact: true }),
          })
          .first()
        if (await dirRow.waitFor({ state: 'visible', timeout: 3_000 }).then(() => true, () => false)) {
          const alreadyHasFile = await page.locator('[data-testid="tree-row"]:visible')
            .filter({
              hasText: dirName === 'handler'
                ? 'webhook.go'
                : 'backoff.go',
            })
            .first()
            .isVisible()
            .catch(() => false)
          if (!alreadyHasFile)
            await dirRow.click()
        }
      }
      await waitFor('backoff.go visible in Changed files tree', async () => {
        const row = page.locator('[data-testid="tree-row"]:visible').filter({ hasText: 'backoff.go' }).first()
        return row.isVisible()
      }, 15_000)
      await page.waitForTimeout(400)

      console.log('Capturing light mode workspace screenshots ...')
      const lightClips = await captureWorkspaceViews(page, 'light')
      console.log('Capturing dark mode workspace screenshots ...')
      await captureWorkspaceViews(page, 'dark')

      console.log('Summary of captured dimensions:')
      console.log(`  coding-agent-chat: ${chatClip.width}x${chatClip.height}`)
      for (const [name, clip] of Object.entries(lightClips))
        console.log(`  ${name}: ${clip.width}x${clip.height}`)

      await context.close()
    }
    finally {
      await browser.close()
    }
  }
  finally {
    await finishCleanup([
      hubUrlForCleanup
        ? closeTestChannels(hubUrlForCleanup)
        : Promise.resolve(),
      secondWorker
        ? stopProcess(secondWorker)
        : Promise.resolve(),
      suiteStop
        ? suiteStop()
        : Promise.resolve(),
    ])
    rmSync(runDir, { recursive: true, force: true })
  }
}

await main()
// User-events WebSocket clients from E2E API helpers keep Bun's event loop
// alive after server teardown completes; exit explicitly once cleanup finishes.
process.exit(0)
