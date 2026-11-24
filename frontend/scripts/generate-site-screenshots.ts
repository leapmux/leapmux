/**
 * Deterministic documentation screenshot generator for https://leapmux.dev.
 *
 * Boots a real isolated LeapMux Hub + Worker + embedded SolidJS frontend
 * (`./leapmux`), registers a second online Worker (`gpu-devbox`), builds a
 * deterministic fixture Git repository with linked worktrees, scripts realistic
 * Claude Code / Codex / Antigravity sessions against the local mock model
 * server (zero network calls, zero API keys), drives a real xterm.js terminal,
 * floating windows, Quake-mode terminal, input queue, Markdown/diff viewers,
 * dialogs, context menus, Preferences panels, mobile layout, and login screen,
 * and captures Retina (2x) light & dark PNGs into `site/static/images/docs/`.
 *
 * Run explicitly by developers when the UI or fixtures change:
 *   task generate-site-screenshots
 */

import type { Browser, BrowserContext, Locator, Page } from '@playwright/test'
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
import {
  askUserQuestionToolCall,
  bashToolCall,
  editToolCall,
  readToolCall,
} from '../tests/e2e/helpers/providerToolCalls'
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
const MOD_KEY = process.platform === 'darwin'
  ? 'Meta'
  : 'Control'

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
  readmePath: string
}

const README_CONTENT = `# payments-api

Checkout retry backoff and signed webhook delivery service.

## Retry policy

Outgoing webhook deliveries use **full-jitter exponential backoff** capped at \`30s\` so retrying clients never synchronize after a downstream outage:

\`\`\`go
ceiling := min(baseBackoff<<min(attempt, 10), maxBackoff)
delay := rand.N(ceiling + 1)
\`\`\`

## Packages

- \`internal/retry\` — jittered backoff calculator and cap enforcement
- \`internal/handler\` — webhook receiver and HMAC-SHA256 verification
- \`internal/auth\` — thread-safe token cache with \`sync.RWMutex\`
`

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
  writeFile(join(paymentsMain, 'README.md'), README_CONTENT)
  git(paymentsMain, 'add', '.')
  git(paymentsMain, 'commit', '-m', 'chore: scaffold payments-api module and docs')

  writeFile(join(paymentsMain, 'internal/retry/backoff.go'), BACKOFF_BEFORE)
  git(paymentsMain, 'add', '.')
  git(paymentsMain, 'commit', '-m', 'feat(retry): add exponential backoff helper')

  writeFile(join(paymentsMain, 'internal/handler/webhook.go'), WEBHOOK_INITIAL)
  writeFile(join(paymentsMain, 'internal/auth/token.go'), TOKEN_INITIAL)
  git(paymentsMain, 'add', '.')
  git(paymentsMain, 'commit', '-m', 'feat(webhook): add handler and token cache')

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
    readmePath: join(wtRetryJitter, 'README.md'),
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

function clampClip(rect: ClipRect, viewport = VIEWPORT): ClipRect {
  const x = Math.max(0, Math.round(rect.x))
  const y = Math.max(0, Math.round(rect.y))
  const right = Math.min(viewport.width, Math.round(rect.x + rect.width))
  const bottom = Math.min(viewport.height, Math.round(rect.y + rect.height))
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

function unionRects(a: ClipRect, b: ClipRect): ClipRect {
  const x = Math.min(a.x, b.x)
  const y = Math.min(a.y, b.y)
  const right = Math.max(a.x + a.width, b.x + b.width)
  const bottom = Math.max(a.y + a.height, b.y + b.height)
  return {
    x,
    y,
    width: right - x,
    height: bottom - y,
  }
}

function padClip(rect: ClipRect, padX: number, padY = padX): ClipRect {
  return clampClip({
    x: rect.x - padX,
    y: rect.y - padY,
    width: rect.width + padX * 2,
    height: rect.height + padY * 2,
  })
}

async function waitForTheme(page: Page, theme: 'light' | 'dark'): Promise<void> {
  await page.emulateMedia({ colorScheme: theme })
  await waitFor(`html[data-theme="${theme}"]`, async () => {
    return (await page.locator('html').getAttribute('data-theme')) === theme
  }, 10_000)
  await page.waitForTimeout(450)
  await page.evaluate(() => document.fonts.ready)
}

async function snap(
  page: Page,
  name: string,
  theme: 'light' | 'dark',
  clip: ClipRect,
  recorded: Record<string, ClipRect>,
): Promise<void> {
  const outPath = join(OUT_DIR, `${name}-${theme}.png`)
  await page.screenshot({ path: outPath, clip, animations: 'disabled' })
  if (theme === 'light')
    recorded[name] = clip
  console.log(`  wrote ${name}-${theme}.png (${clip.width}x${clip.height} @2x)`)
}

async function typeComposerAndSend(page: Page, text: string): Promise<void> {
  const editor = page.locator('[data-testid="composer-editor"]:visible .ProseMirror[contenteditable="true"]').first()
  await editor.waitFor({ state: 'visible', timeout: 20_000 })
  await editor.locator(':scope > :first-child').click()
  await page.keyboard.insertText(text)
  await page.keyboard.press(`${MOD_KEY}+Enter`)
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

async function sendTerminalLine(page: Page, container: Locator, command: string): Promise<void> {
  const activePane = container.locator('[data-terminal-id][data-active="true"]').first()
  const target = (await activePane.count()) > 0
    ? activePane
    : container
  const xterm = target.locator('.xterm').first()
  await xterm.click({
    position: { x: 40, y: 40 },
  })
  const textarea = target.locator('.xterm-helper-textarea').first()
  await textarea.focus()
  await page.keyboard.type(command, { delay: 10 })
  await page.keyboard.press('Enter')
}

async function waitForLayoutSaved(page: Page): Promise<void> {
  await page.evaluate(() => new Promise<void>((resolveSaved) => {
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
}

async function centerCanvasClip(page: Page): Promise<ClipRect> {
  const resizeHandles = page.locator('[data-testid="resize-handle"]')
  const leftHandle = await requireBox(resizeHandles.nth(0), 'left resize handle')
  const rightHandle = await requireBox(resizeHandles.nth(1), 'right resize handle')
  return clampClip({
    x: leftHandle.x - 24,
    y: 0,
    width: (rightHandle.x + rightHandle.width + 24) - (leftHandle.x - 24),
    height: VIEWPORT.height,
  })
}

async function dragPointer(
  page: Page,
  fromX: number,
  fromY: number,
  toX: number,
  toY: number,
): Promise<void> {
  await page.mouse.move(fromX, fromY)
  await page.mouse.down()
  await page.mouse.move(toX, toY, { steps: 10 })
  await page.mouse.up()
}

function futureLocalDateTime(): string {
  const target = new Date(Date.now() + 24 * 60 * 60 * 1000)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${target.getFullYear()}-${pad(target.getMonth() + 1)}-${pad(target.getDate())}T09:00`
}

async function resetLeftSidebarScroll(page: Page): Promise<void> {
  await page.evaluate(() => {
    for (const el of document.querySelectorAll('*')) {
      if (el.scrollLeft > 0)
        el.scrollLeft = 0
    }
  })
}

async function captureCleanWorkspaceViews(
  page: Page,
  theme: 'light' | 'dark',
  wsOAuth: string,
  recorded: Record<string, ClipRect>,
): Promise<void> {
  await waitForTheme(page, theme)
  await resetLeftSidebarScroll(page)
  await page.waitForTimeout(200)

  const tiles = page.locator('[data-testid="tile"]')
  const tile1 = await requireBox(tiles.nth(1), 'tile 1 (diff viewer)')
  const tile2 = await requireBox(tiles.nth(2), 'tile 2 (terminal)')
  const resizeHandles = page.locator('[data-testid="resize-handle"]')
  const leftHandle = await requireBox(resizeHandles.nth(0), 'left resize handle')
  const oauthRow = page.locator(`[data-testid="workspace-item-${wsOAuth}"]:visible`).first()
  const oauthBox = await requireBox(oauthRow, 'OAuth workspace row')
  const centerClip = await centerCanvasClip(page)

  await snap(page, 'sidebar-workspaces', theme, clampClip({
    x: 0,
    y: 0,
    width: leftHandle.x + leftHandle.width + 150,
    height: VIEWPORT.height,
  }), recorded)

  await snap(page, 'sidebar-branch-tree', theme, clampClip({
    x: 0,
    y: 0,
    width: leftHandle.x + leftHandle.width + 6,
    height: oauthBox.y + oauthBox.height + 24,
  }), recorded)

  await snap(page, 'terminal-pane', theme, clampClip({
    x: tile2.x - 24,
    y: tile2.y - 28,
    width: tile2.width + 48,
    height: tile2.height + 52,
  }), recorded)

  await snap(page, 'tabs-tiling-layout', theme, centerClip, recorded)

  await snap(page, 'file-browser-diff', theme, clampClip({
    x: tile1.x - 24,
    y: 0,
    width: VIEWPORT.width - (tile1.x - 24),
    height: tile1.y + tile1.height + 72,
  }), recorded)
}

async function openFloatingLogWindow(
  page: Page,
  channel: Awaited<ReturnType<typeof getTestChannel>>,
  workerId: string,
  wsMain: string,
  tile2Id: string,
  wtRetryJitter: string,
): Promise<void> {
  const tile2 = page.locator('[data-testid="tile"]').nth(2)
  await waitFor('floating candidate terminal opened in Tile 2', async () => {
    await channel.callWorker(
      workerId,
      WorkerTerminalService.method.openTerminal,
      {
        workingDir: wtRetryJitter,
        title: 'git log',
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

  const gitLogTab = tile2.locator('[data-testid="tab"]').filter({ hasText: 'git log' }).first()
  await gitLogTab.waitFor({ state: 'visible', timeout: 15_000 })
  await gitLogTab.click()
  await waitFor('floating terminal startup overlay gone', async () => {
    return (await tile2.locator('[data-testid="terminal-startup-overlay"]:visible').count()) === 0
  }, 20_000)

  await waitFor('git log shell ready', async () => {
    await sendTerminalLine(page, tile2, 'echo LM_FLOAT_READY')
    const txt = await getActiveTerminalText(page)
    return txt.includes('LM_FLOAT_READY')
  }, 20_000, 400)

  const logScript = [
    `export PS1=$'\\e[36mpayments-api\\e[0m \\e[32m❯\\e[0m '`,
    'clear',
    `printf '\\e[36mpayments-api\\e[0m \\e[32m❯\\e[0m git log --oneline -n 3\\n'`,
    `git log --format='%C(yellow)%h%C(reset) %s' -n 3`,
  ].join(' && ')
  await sendTerminalLine(page, tile2, logScript)
  await waitFor('git log rendered in floating terminal', async () => {
    const txt = await getActiveTerminalText(page)
    return txt.includes('feat(webhook)') && txt.includes('feat(retry)')
  }, 20_000, 300)

  // Pop out the `git log` tab into a real floating window.
  const popSaved = waitForLayoutSaved(page)
  await tile2.locator('[data-testid="pop-out-button"]').click()
  const floatWin = page.locator('[data-testid="floating-window"]').first()
  await floatWin.waitFor({ state: 'visible', timeout: 10_000 })
  await popSaved

  // Resize compactly using the SE resize handle (second-to-last child div in
  // FloatingWindowContainer's RESIZE_HANDLES) and position cleanly in the
  // bottom-right corner of the page so the underlying tiled panes and File
  // Browser remain legible around and faintly behind it.
  const layer = page.locator('[data-testid="floating-window-layer"]').first()
  const layerBox = await requireBox(layer, 'floating window layer')
  const desiredWidth = 460
  const desiredHeight = 250

  const seHandle = floatWin.locator(':scope > div').nth(-2)
  const seBox = await requireBox(seHandle, 'floating window SE resize handle')
  const beforeResize = await requireBox(floatWin, 'floating window before resize')
  const resizeSaved = waitForLayoutSaved(page)
  await dragPointer(
    page,
    seBox.x + seBox.width / 2,
    seBox.y + seBox.height / 2,
    beforeResize.x + desiredWidth,
    beforeResize.y + desiredHeight,
  )
  await resizeSaved
  await page.waitForTimeout(200)

  const afterResize = await requireBox(floatWin, 'floating window after resize')
  const titleBar = floatWin.locator('[data-testid="floating-window-titlebar"]').first()
  const titleBox = await requireBox(titleBar, 'floating window titlebar')
  const targetX = layerBox.x + layerBox.width - afterResize.width - 24
  const targetY = layerBox.y + layerBox.height - afterResize.height - 24
  const moveSaved = waitForLayoutSaved(page)
  await dragPointer(
    page,
    titleBox.x + titleBox.width / 2,
    titleBox.y + titleBox.height / 2,
    titleBox.x + titleBox.width / 2 + (targetX - afterResize.x),
    titleBox.y + titleBox.height / 2 + (targetY - afterResize.y),
  )
  await moveSaved
  await page.waitForTimeout(200)

  // Lower opacity to 85% via 3 wheel-down ticks on the floating titlebar so
  // the semi-transparent overlay effect is clearly visible in hero-workspace
  // and floating-window screenshots.
  const updatedTitleBox = await requireBox(titleBar, 'floating titlebar for opacity wheel')
  await page.mouse.move(updatedTitleBox.x + updatedTitleBox.width / 2, updatedTitleBox.y + updatedTitleBox.height / 2)
  const opacitySaved = waitForLayoutSaved(page)
  for (let i = 0; i < 3; i++) {
    await page.mouse.wheel(0, 120)
    await page.waitForTimeout(80)
  }
  await waitFor('floating window opacity lowered', async () => {
    const opacity = await floatWin.evaluate(el => Number.parseFloat(getComputedStyle(el).opacity || '1'))
    return opacity <= 0.87 && opacity >= 0.78
  }, 10_000)
  await opacitySaved
  await page.waitForTimeout(300)
}

async function closeFloatingLogWindow(page: Page): Promise<void> {
  const floatWin = page.locator('[data-testid="floating-window"]').first()
  if (!await floatWin.isVisible().catch(() => false))
    return
  await floatWin.locator('[data-testid="tab-close"]').first().click()
  await floatWin.waitFor({ state: 'hidden', timeout: 10_000 })
  await page.waitForTimeout(250)
}

async function captureFloatingViews(
  page: Page,
  theme: 'light' | 'dark',
  recorded: Record<string, ClipRect>,
): Promise<void> {
  await waitForTheme(page, theme)
  await resetLeftSidebarScroll(page)
  await page.waitForTimeout(200)
  await snap(page, 'hero-workspace', theme, {
    x: 0,
    y: 0,
    width: VIEWPORT.width,
    height: VIEWPORT.height,
  }, recorded)
  const resizeHandles = page.locator('[data-testid="resize-handle"]')
  const leftHandle = await requireBox(resizeHandles.nth(0), 'left resize handle')
  const floatClip = clampClip({
    x: leftHandle.x - 24,
    y: 0,
    width: VIEWPORT.width - (leftHandle.x - 24),
    height: VIEWPORT.height,
  })
  await snap(page, 'floating-window', theme, floatClip, recorded)
}

async function captureQuakeTerminalViews(
  page: Page,
  recorded: Record<string, ClipRect>,
): Promise<void> {
  await waitForTheme(page, 'light')
  // Focus Tile 0 so the Quake terminal binds to wtRetryJitter.
  await page.locator('[data-testid="tile"]').nth(0).click({
    position: { x: 24, y: 12 },
  })
  await page.keyboard.press('Control+Backquote')
  const quakePanel = page.locator('[data-testid="quake-panel"][data-quake-open="true"]').first()
  await quakePanel.waitFor({ state: 'visible', timeout: 15_000 })
  await quakePanel.locator('.xterm').waitFor({ state: 'visible', timeout: 20_000 })
  await waitFor('quake terminal startup overlay gone', async () => {
    return (await quakePanel.locator('[data-testid="terminal-startup-overlay"]').count()) === 0
  }, 20_000)

  await waitFor('quake shell ready', async () => {
    await sendTerminalLine(page, quakePanel, 'echo LM_QUAKE_READY')
    const txt = await quakePanel.locator('.xterm-rows').textContent().catch(() => '')
    return Boolean(txt && txt.includes('LM_QUAKE_READY'))
  }, 20_000, 400)

  const quakeScript = [
    `export PS1=$'\\e[36mpayments-api\\e[0m \\e[35m(quake)\\e[0m \\e[32m❯\\e[0m '`,
    'clear',
    `printf '\\e[36mpayments-api\\e[0m \\e[35m(quake)\\e[0m \\e[32m❯\\e[0m git diff --stat\\n'`,
    'git diff --stat',
  ].join(' && ')
  await sendTerminalLine(page, quakePanel, quakeScript)
  await waitFor('git diff --stat rendered in quake panel', async () => {
    const txt = await quakePanel.locator('.xterm-rows').textContent().catch(() => '')
    return Boolean(txt && txt.includes('backoff.go') && txt.includes('webhook.go'))
  }, 20_000, 300)

  const centerClip = await centerCanvasClip(page)
  await page.waitForTimeout(300)
  await snap(page, 'quake-terminal', 'light', centerClip, recorded)
  await waitForTheme(page, 'dark')
  await snap(page, 'quake-terminal', 'dark', centerClip, recorded)
  await waitForTheme(page, 'light')

  await page.keyboard.press('Control+Backquote')
  await waitFor('quake panel closed', async () => {
    const open = await page.locator('[data-testid="quake-panel"]').first().getAttribute('data-quake-open').catch(() => 'false')
    return open !== 'true'
  }, 10_000)
  await page.waitForTimeout(300)
}

async function captureMarkdownViewerViews(
  page: Page,
  channel: Awaited<ReturnType<typeof getTestChannel>>,
  workerId: string,
  wsMain: string,
  wtRetryJitter: string,
  readmePath: string,
  recorded: Record<string, ClipRect>,
): Promise<void> {
  const tile1 = page.locator('[data-testid="tile"]').nth(1)
  const currentTile1Id = await tile1.getAttribute('data-tile-id')
  if (!currentTile1Id)
    throw new Error('Tile 1 has no data-tile-id attribute for Markdown viewer')

  await waitFor('README.md tab registered in Tile 1', async () => {
    await channel.callWorker(
      workerId,
      WorkerPrivateService.method.registerTabPayload,
      {
        payload: {
          workingDir: wtRetryJitter,
          kind: {
            case: 'file',
            value: { filePath: readmePath },
          },
        },
        creation: {
          tabId: randomUUID(),
          placement: {
            workspaceId: wsMain,
            destination: { case: 'tileId', value: currentTile1Id },
            insertion: TabInsertion.LAST,
          },
          presentation: {
            fileViewMode: TabFileViewMode.WORKING,
          },
        },
      },
    )
    return true
  }, 10_000, 200)

  const readmeTab = tile1.locator('[data-testid="tab"]').filter({ hasText: 'README.md' }).first()
  await readmeTab.waitFor({ state: 'visible', timeout: 15_000 })
  await readmeTab.click()
  const sideBySideBtn = tile1.getByRole('button', { name: 'Side-by-side view' }).first()
  await sideBySideBtn.waitFor({ state: 'visible', timeout: 15_000 })
  await sideBySideBtn.click()
  await waitFor('Markdown side-by-side rendered', async () => {
    const text = await tile1.textContent()
    return Boolean(text && text.includes('Retry policy') && text.includes('## Retry policy'))
  }, 15_000)

  const tile1Box = await requireBox(tile1, 'tile 1 markdown side-by-side')
  const mdClip = clampClip({
    x: tile1Box.x - 16,
    y: 0,
    width: VIEWPORT.width - (tile1Box.x - 16),
    height: tile1Box.y + tile1Box.height + 24,
  })
  await waitForTheme(page, 'light')
  await snap(page, 'file-viewer-markdown', 'light', mdClip, recorded)
  await waitForTheme(page, 'dark')
  await snap(page, 'file-viewer-markdown', 'dark', mdClip, recorded)
  await waitForTheme(page, 'light')

  // Close README.md tab so Tile 1 returns to backoff.go diff.
  await readmeTab.locator('[data-testid="tab-close"]').click()
  await readmeTab.waitFor({ state: 'hidden', timeout: 10_000 })
  await page.waitForTimeout(250)
}

async function captureDialogsAndMenus(
  page: Page,
  theme: 'light' | 'dark',
  wsMain: string,
  recorded: Record<string, ClipRect>,
): Promise<void> {
  await waitForTheme(page, theme)
  await resetLeftSidebarScroll(page)

  // 1. Workspace row context menu (left sidebar).
  const wsRow = page.locator(`[data-testid="workspace-item-${wsMain}"]:visible`).first()
  await wsRow.hover()
  await wsRow.getByTestId('workspace-row-menu-trigger').click()
  await wsRow.getByRole('menuitem', { name: 'Rename', exact: true }).waitFor({ state: 'visible', timeout: 10_000 })
  await page.waitForTimeout(150)
  const wsMenu = wsRow.locator('menu[popover]:visible').first()
  const wsRowBox = await requireBox(wsRow, 'workspace row')
  const wsMenuBox = await requireBox(wsMenu, 'workspace context menu')
  await snap(page, 'workspace-context-menu', theme, clampClip({
    x: 0,
    y: wsRowBox.y - 44,
    width: Math.max(wsRowBox.x + wsRowBox.width + 80, wsMenuBox.x + wsMenuBox.width + 48),
    height: (wsMenuBox.y + wsMenuBox.height + 48) - (wsRowBox.y - 44),
  }), recorded)
  await page.keyboard.press('Escape')
  await wsMenu.waitFor({ state: 'hidden', timeout: 5_000 }).catch(() => {})

  // 2. Branch group context menu (left sidebar tab tree).
  const wsChildren = page.locator(`[data-testid="workspace-children-${wsMain}"]:visible`).first()
  const branchRow = wsChildren.locator('[data-testid="tab-tree-branch-group"]:visible').first()
  await branchRow.hover()
  await branchRow.locator('[aria-expanded]').first().click()
  await page.getByRole('menuitem', { name: 'Switch to branch...' }).first().waitFor({ state: 'visible', timeout: 10_000 })
  await page.waitForTimeout(150)
  const branchMenu = page.locator('menu[popover]:visible').first()
  const branchRowBox = await requireBox(branchRow, 'branch row')
  const branchMenuBox = await requireBox(branchMenu, 'branch context menu')
  const branchTop = Math.max(0, Math.min(branchRowBox.y - 36, branchMenuBox.y - 24))
  await snap(page, 'branch-context-menu', theme, clampClip({
    x: 0,
    y: branchTop,
    width: Math.max(branchRowBox.x + branchRowBox.width + 80, branchMenuBox.x + branchMenuBox.width + 48),
    height: Math.min(VIEWPORT.height - branchTop, (branchMenuBox.y + branchMenuBox.height + 36) - branchTop),
  }), recorded)
  await page.keyboard.press('Escape')
  await branchMenu.waitFor({ state: 'hidden', timeout: 5_000 }).catch(() => {})

  // 3. File browser context menu (right sidebar Files panel).
  const fileRow = page.locator('[data-testid="tree-row"]:visible').filter({ hasText: 'backoff.go' }).first()
  await fileRow.hover()
  await fileRow.locator('[data-testid="tree-context-button"]').click()
  await fileRow.locator('[data-testid="tree-copy-path-button"]:visible').first().waitFor({ state: 'visible', timeout: 10_000 })
  await page.waitForTimeout(150)
  const fileMenu = fileRow.locator('menu[popover]:visible').first()
  const fileRowBox = await requireBox(fileRow, 'file row')
  const fileMenuBox = await requireBox(fileMenu, 'file context menu')
  const fileUnion = unionRects(fileRowBox, fileMenuBox)
  await snap(page, 'file-context-menu', theme, clampClip({
    x: fileUnion.x - 48,
    y: fileUnion.y - 60,
    width: VIEWPORT.width - (fileUnion.x - 48),
    height: fileUnion.height + 108,
  }), recorded)
  await page.keyboard.press('Escape')
  await fileMenu.waitFor({ state: 'hidden', timeout: 5_000 }).catch(() => {})

  // 4. Worker row context menu (left sidebar Workers section).
  const gpuRow = page.locator('[data-testid="worker-row"]:visible').filter({ hasText: 'gpu-devbox' }).first()
  await gpuRow.hover()
  await gpuRow.locator('[aria-expanded]').first().click()
  await page.getByRole('menuitem', { name: 'Deregister...', exact: true }).first().waitFor({ state: 'visible', timeout: 10_000 })
  await page.waitForTimeout(150)
  const workerMenu = page.locator('menu[popover]:visible').first()
  const gpuRowBox = await requireBox(gpuRow, 'gpu-devbox worker row')
  const workerMenuBox = await requireBox(workerMenu, 'worker context menu')
  const workerUnion = unionRects(gpuRowBox, workerMenuBox)
  await snap(page, 'worker-context-menu', theme, clampClip({
    x: 0,
    y: workerUnion.y - 44,
    width: workerUnion.x + workerUnion.width + 48,
    height: workerUnion.height + 88,
  }), recorded)
  await page.keyboard.press('Escape')
  await workerMenu.waitFor({ state: 'hidden', timeout: 5_000 }).catch(() => {})

  // 5. Make grid popover (tile action strip — right-anchored around the popover).
  const tile0 = page.locator('[data-testid="tile"]').nth(0)
  const makeGridBtn = tile0.locator('[data-testid="make-grid"]').first()
  const makeGridBox = await requireBox(makeGridBtn, 'make-grid button')
  await makeGridBtn.click()
  const gridPopover = page.locator('[data-testid="grid-size-popover"]:visible').first()
  await gridPopover.waitFor({ state: 'visible', timeout: 10_000 })
  // Hover 3x4 cell so the grid preview highlights crisply.
  await gridPopover.locator('[data-testid="grid-size-cell-2-3"]').hover()
  await page.waitForTimeout(150)
  const gridPopoverBox = await requireBox(gridPopover, 'grid size popover')
  const gridUnion = unionRects(makeGridBox, gridPopoverBox)
  await snap(page, 'tile-grid-popover', theme, clampClip({
    x: gridUnion.x - 96,
    y: Math.max(0, gridUnion.y - 28),
    width: gridUnion.width + 156,
    height: gridUnion.height + 68,
  }), recorded)
  await page.keyboard.press('Escape')
  await gridPopover.waitFor({ state: 'hidden', timeout: 5_000 }).catch(() => {})

  // 6. Tab bar "+" (More options) menu.
  const tabMoreBtn = tile0.locator('[data-testid="tab-more-menu"]:visible, [data-testid="collapsed-new-tab-button"]:visible').first()
  const tabMoreBox = await requireBox(tabMoreBtn, 'tab-more-menu button')
  await tabMoreBtn.click()
  const tabMoreMenu = page.locator('menu[popover]:visible').first()
  await tabMoreMenu.waitFor({ state: 'visible', timeout: 10_000 })
  await page.waitForTimeout(150)
  const tabMoreMenuBox = await requireBox(tabMoreMenu, 'tab bar more menu')
  const tabMoreUnion = unionRects(tabMoreBox, tabMoreMenuBox)
  await snap(page, 'tab-bar-more-menu', theme, clampClip({
    x: tabMoreUnion.x - 110,
    y: Math.max(0, tabMoreUnion.y - 24),
    width: tabMoreUnion.width + 170,
    height: tabMoreUnion.height + 56,
  }), recorded)
  await page.keyboard.press('Escape')
  await tabMoreMenu.waitFor({ state: 'hidden', timeout: 5_000 }).catch(() => {})

  // 7. Composer "[+]" menu + status bar chips (ensure Tile 0 agent tab is focused
  // so the shared bottom agent composer is mounted).
  await tile0.click({
    position: { x: 24, y: 12 },
  })
  const plusTrigger = page.locator('[data-testid="composer-plus-trigger"]:visible').first()
  await plusTrigger.waitFor({ state: 'visible', timeout: 10_000 })
  await plusTrigger.click()
  const plusPopover = page.locator('[data-testid="composer-plus-popover"]:visible').first()
  await plusPopover.waitFor({ state: 'visible', timeout: 10_000 })
  await page.waitForTimeout(150)
  const plusPopoverBox = await requireBox(plusPopover, 'composer plus popover')
  const statusBar = page.locator('[data-testid="composer-status-bar"]:visible').first()
  const statusBarBox = await requireBox(statusBar, 'composer status bar')
  const composerUnion = unionRects(plusPopoverBox, statusBarBox)
  await snap(page, 'composer-plus-menu', theme, clampClip({
    x: composerUnion.x - 32,
    y: composerUnion.y - 36,
    width: composerUnion.width + 64,
    height: VIEWPORT.height - (composerUnion.y - 36),
  }), recorded)
  await page.keyboard.press('Escape')
  await plusPopover.waitFor({ state: 'hidden', timeout: 5_000 }).catch(() => {})

  // 8. New workspace dialog (pre-filled with payments-api so Git options show).
  const inProgressMenuBtn = page.locator('[data-testid="sidebar-section-menu-workspaces_in_progress"]:visible').first()
  await inProgressMenuBtn.click()
  const repoItem = page.locator('[data-testid="sidebar-new-workspace-repo"]:visible').filter({ hasText: 'payments-api' }).first()
  if (await repoItem.waitFor({ state: 'visible', timeout: 4_000 }).then(() => true, () => false))
    await repoItem.click()
  else
    await page.locator('[data-testid="sidebar-new-workspace"]:visible').first().click()
  const newWsDialog = page.locator('dialog[open]').first()
  await newWsDialog.waitFor({ state: 'visible', timeout: 10_000 })
  const titleInput = newWsDialog.getByTestId('title-input')
  if (await titleInput.isVisible().catch(() => false))
    await titleInput.fill('Rate limit middleware')
  await page.waitForTimeout(450)
  await snap(page, 'new-workspace-dialog', theme, padClip(await requireBox(newWsDialog, 'new workspace dialog'), 48, 36), recorded)
  await page.keyboard.press('Escape')
  await newWsDialog.waitFor({ state: 'hidden', timeout: 5_000 }).catch(() => {})

  // 9. New agent dialog (showing Create new worktree + Worktree path preview).
  await tabMoreBtn.click()
  await page.getByRole('menuitem', { name: /New agent\.\.\./ }).first().click()
  const newAgentDialog = page.locator('dialog[open]').first()
  await newAgentDialog.waitFor({ state: 'visible', timeout: 10_000 })
  const createWtRadio = newAgentDialog.getByLabel('Create new worktree')
  await createWtRadio.waitFor({ state: 'visible', timeout: 10_000 })
  await createWtRadio.click()
  const branchNameInput = newAgentDialog.getByPlaceholder('feature-branch')
  await branchNameInput.waitFor({ state: 'visible', timeout: 5_000 })
  await branchNameInput.fill('feat/rate-limit')
  await page.waitForTimeout(350)
  await newAgentDialog.evaluate((dialog) => {
    for (const el of dialog.querySelectorAll('*')) {
      if (el.scrollHeight > el.clientHeight)
        el.scrollTop = 0
    }
  })
  await snap(page, 'new-agent-dialog', theme, padClip(await requireBox(newAgentDialog, 'new agent dialog'), 48, 36), recorded)
  await page.keyboard.press('Escape')
  await newAgentDialog.waitFor({ state: 'hidden', timeout: 5_000 }).catch(() => {})

  // 10. New terminal dialog.
  await tabMoreBtn.click()
  await page.getByRole('menuitem', { name: /New terminal\.\.\./ }).first().click()
  const newTermDialog = page.locator('dialog[open]').first()
  await newTermDialog.waitFor({ state: 'visible', timeout: 10_000 })
  await page.waitForTimeout(450)
  await newTermDialog.evaluate((dialog) => {
    for (const el of dialog.querySelectorAll('*')) {
      if (el.scrollHeight > el.clientHeight)
        el.scrollTop = 0
    }
  })
  await snap(page, 'new-terminal-dialog', theme, padClip(await requireBox(newTermDialog, 'new terminal dialog'), 48, 36), recorded)
  await page.keyboard.press('Escape')
  await newTermDialog.waitFor({ state: 'hidden', timeout: 5_000 }).catch(() => {})

  // 11. Change branch dialog (opened on Create new worktree from branch menu).
  await branchRow.hover()
  await branchRow.locator('[aria-expanded]').first().click()
  await page.getByRole('menuitem', { name: 'Create new worktree...' }).first().click()
  const changeBranchDialog = page.locator('dialog[open]').first()
  await changeBranchDialog.waitFor({ state: 'visible', timeout: 10_000 })
  const cbInput = changeBranchDialog.getByPlaceholder('feature-branch')
  if (await cbInput.waitFor({ state: 'visible', timeout: 5_000 }).then(() => true, () => false))
    await cbInput.fill('feat/retry-metrics')
  await page.waitForTimeout(350)
  await snap(page, 'change-branch-dialog', theme, padClip(await requireBox(changeBranchDialog, 'change branch dialog'), 48, 36), recorded)
  await page.keyboard.press('Escape')
  await changeBranchDialog.waitFor({ state: 'hidden', timeout: 5_000 }).catch(() => {})

  // 12. Close last tab dialog (triggered on dirty linked worktree feat/webhook-signatures).
  const webhookLeaf = wsChildren.locator('[data-testid="tab-tree-leaf"]:visible').filter({ hasText: 'Webhook HMAC' }).first()
  await webhookLeaf.hover()
  await webhookLeaf.locator('[data-testid="workspace-tab-close"]').click()
  const closeLastDialog = page.getByRole('dialog', { name: 'Close last tab' })
  await closeLastDialog.waitFor({ state: 'visible', timeout: 10_000 })
  await page.waitForTimeout(250)
  await snap(page, 'close-last-tab-dialog', theme, padClip(await requireBox(closeLastDialog, 'close last tab dialog'), 48, 36), recorded)
  await closeLastDialog.getByRole('button', { name: 'Cancel', exact: true }).click()
  await closeLastDialog.waitFor({ state: 'hidden', timeout: 5_000 }).catch(() => {})

  // 13. Register worker dialog (deterministic hub URL + registration key).
  await page.locator('[data-testid="sidebar-register-worker"]:visible').first().click()
  const regDialog = page.getByRole('dialog', { name: 'Register worker' })
  await regDialog.waitFor({ state: 'visible', timeout: 10_000 })
  await regDialog.getByTestId('registration-command').waitFor({ state: 'visible', timeout: 10_000 })
  await page.waitForTimeout(200)
  await snap(page, 'register-worker-dialog', theme, padClip(await requireBox(regDialog, 'register worker dialog'), 48, 36), recorded)
  await regDialog.getByRole('button', { name: 'Close', exact: true }).click()
  await regDialog.waitFor({ state: 'hidden', timeout: 5_000 }).catch(() => {})

  // 14. Preferences dialog (Appearance, Account, Apps, Agent Skills, Keyboard Shortcuts, Administration).
  await page.getByTestId('app-menu-trigger').first().click()
  await page.getByRole('menuitem', { name: 'Preferences' }).click()
  const prefsDialog = page.getByRole('dialog', { name: 'Preferences' })
  await prefsDialog.waitFor({ state: 'visible', timeout: 10_000 })
  const prefsClip = padClip(await requireBox(prefsDialog, 'preferences dialog'), 40, 32)

  // 14a. Appearance (default overview of Preferences dialog).
  await prefsDialog.getByTestId('preferences-nav-appearance').click()
  await prefsDialog.locator('[data-setting-id="appearance.theme"]').waitFor({ state: 'visible', timeout: 10_000 })
  await page.waitForTimeout(200)
  await snap(page, 'preferences-dialog', theme, prefsClip, recorded)

  // 14b. Account.
  await prefsDialog.getByTestId('preferences-nav-account').click()
  await prefsDialog.locator('[data-setting-id="account.profile"]').waitFor({ state: 'visible', timeout: 10_000 })
  await page.waitForTimeout(200)
  await snap(page, 'preferences-account', theme, prefsClip, recorded)

  // 14c. Apps.
  await prefsDialog.getByTestId('preferences-nav-apps').click()
  await prefsDialog.locator('[data-setting-id="account.connectedApps"]').waitFor({ state: 'visible', timeout: 10_000 })
  await page.waitForTimeout(200)
  await snap(page, 'preferences-apps', theme, prefsClip, recorded)

  // 14d. Chat & Composer -> Agent skills.
  await prefsDialog.getByTestId('preferences-nav-chat').click()
  const skillsControl = prefsDialog.getByTestId('agent-skills-control')
  await skillsControl.waitFor({ state: 'visible', timeout: 15_000 })
  await skillsControl.scrollIntoViewIfNeeded()
  await page.waitForTimeout(350)
  await snap(page, 'preferences-agent-skills', theme, prefsClip, recorded)

  // 14e. Keyboard Shortcuts.
  await prefsDialog.getByTestId('preferences-nav-shortcuts').click()
  await prefsDialog.locator('[data-setting-id="shortcuts.keybindings"]').waitFor({ state: 'visible', timeout: 10_000 })
  await page.waitForTimeout(200)
  await snap(page, 'preferences-keyboard-shortcuts', theme, prefsClip, recorded)

  // 14f. Administration -> Network access (trusted reverse proxies).
  await prefsDialog.getByTestId('preferences-nav-admin-network').click()
  await page.waitForTimeout(250)
  await snap(page, 'preferences-admin', theme, prefsClip, recorded)

  await page.keyboard.press('Escape')
  await prefsDialog.waitFor({ state: 'hidden', timeout: 5_000 }).catch(() => {})
}

async function captureInputQueueAndQuestionViews(
  page: Page,
  server: { hubUrl: string, adminToken: string, workerId: string },
  wsMain: string,
  wtRetryJitter: string,
  recorded: Record<string, ClipRect>,
): Promise<void> {
  await waitForTheme(page, 'light')

  // Close Tile 2 (zsh) and Tile 1 (backoff.go) so the agent pane returns to
  // full 992px center width for clean, unwrapped input-queue and question cards.
  while ((await page.locator('[data-testid="tile"]').count()) > 1) {
    const lastTile = page.locator('[data-testid="tile"]').last()
    const tabClose = lastTile.locator('[data-testid="tab-close"]').first()
    if (await tabClose.isVisible().catch(() => false)) {
      await tabClose.click()
      await page.waitForTimeout(250)
    }
    const tileClose = lastTile.locator('[data-testid="close-tile"]').first()
    if (await tileClose.isVisible().catch(() => false)) {
      await tileClose.click()
      const closeAll = page.getByRole('button', { name: 'Close all tabs', exact: true })
      if (await closeAll.waitFor({ state: 'visible', timeout: 1_500 }).then(() => true, () => false))
        await closeAll.click()
    }
    await page.waitForTimeout(250)
  }

  // Ensure the primary Claude Code tab is focused after collapsing split tiles.
  const claudeTabAfterCollapse = page.locator('[data-testid="tab"]').filter({ hasText: 'Retry jitter' }).first()
  await claudeTabAfterCollapse.click()

  // Trigger a held active turn (delayMs: 45_000) so the agent stays genuinely
  // working while we stack immediate + scheduled items in the input queue.
  await typeComposerAndSend(
    page,
    'Run the retry stress benchmark across 16 goroutines and report any allocations.',
  )
  await waitFor('agent working indicator visible', async () => {
    const pauseBtn = page.locator('[data-testid="queue-pause-button"]:visible').first()
    return pauseBtn.isVisible().catch(() => false)
  }, 15_000)

  // Queue two immediate follow-up messages while the turn is running.
  await typeComposerAndSend(
    page,
    'Use a deterministic PRNG seed in the unit tests so CI never flakes.',
  )
  await typeComposerAndSend(
    page,
    'Document the full-jitter backoff formula and maxBackoff cap in README.md.',
  )

  const queueRoot = page.locator('[data-testid="agent-input-queue"]:visible').first()
  await queueRoot.waitFor({ state: 'visible', timeout: 15_000 })
  await waitFor('two immediate items queued with Steer action', async () => {
    const text = await queueRoot.textContent()
    return Boolean(text && text.includes('PRNG seed') && text.includes('README.md') && text.includes('Steer'))
  }, 15_000)

  // Open the Schedule send popover, enter tomorrow 09:00, capture it in both
  // themes, then apply and queue a scheduled message.
  const scheduleTrigger = page.locator('[data-testid="schedule-send-trigger"]:visible').first()
  await scheduleTrigger.click()
  const schedulePopover = page.locator('[data-testid="schedule-send-popover"]:visible').first()
  await schedulePopover.waitFor({ state: 'visible', timeout: 10_000 })
  const scheduleField = schedulePopover.locator('[data-testid="schedule-send-field"]')
  await scheduleField.fill(futureLocalDateTime())
  await page.waitForTimeout(150)

  const schedulePopoverBox = await requireBox(schedulePopover, 'schedule send popover')
  const scheduleTriggerBox = await requireBox(scheduleTrigger, 'schedule send trigger')
  const scheduleUnion = unionRects(schedulePopoverBox, scheduleTriggerBox)
  const scheduleClip = clampClip({
    x: scheduleUnion.x - 64,
    y: scheduleUnion.y - 40,
    width: scheduleUnion.width + 128,
    height: Math.min(VIEWPORT.height - (scheduleUnion.y - 40), scheduleUnion.height + 96),
  })
  await snap(page, 'schedule-send-popover', 'light', scheduleClip, recorded)
  await waitForTheme(page, 'dark')
  await snap(page, 'schedule-send-popover', 'dark', scheduleClip, recorded)
  await waitForTheme(page, 'light')

  await schedulePopover.locator('[data-testid="schedule-send-apply"]').click()
  await schedulePopover.waitFor({ state: 'hidden', timeout: 5_000 }).catch(() => {})
  await typeComposerAndSend(
    page,
    'Run the full integration test suite before opening the pull request.',
  )
  await waitFor('scheduled item visible in input queue', async () => {
    const text = await queueRoot.textContent()
    return Boolean(text && text.includes('Scheduled for'))
  }, 15_000)

  // Capture input-queue (lower portion of the center agent pane showing the
  // working indicator, immediate + scheduled queue rows with Steer/Move/Edit/
  // Delete controls, Pause Queue button, and composer).
  const centerClip = await centerCanvasClip(page)
  const queueBox = await requireBox(queueRoot, 'agent input queue')
  const queueClipTop = Math.max(0, queueBox.y - 120)
  const inputQueueClip = clampClip({
    x: centerClip.x,
    y: queueClipTop,
    width: centerClip.width,
    height: VIEWPORT.height - queueClipTop,
  })
  await snap(page, 'input-queue', 'light', inputQueueClip, recorded)
  await waitForTheme(page, 'dark')
  await snap(page, 'input-queue', 'dark', inputQueueClip, recorded)
  await waitForTheme(page, 'light')

  // Open a fresh Claude Code tab to capture a clean AskUserQuestion control
  // banner without interrupting or mixing with the held benchmark turn.
  const questionAgentId = await openAgentViaAPI(
    server,
    wsMain,
    deliberateWorkingDir(wtRetryJitter, 'documentation screenshot question banner'),
    {
      ...agentOpenOptions(AgentProvider.CLAUDE_CODE, {
        optionValues: { permissionMode: 'default' },
      }),
      title: 'Jitter policy',
    },
  )
  const questionTab = page.locator(`[data-testid="tab"][data-tab-id="${questionAgentId}"]:visible`).first()
  await questionTab.waitFor({ state: 'visible', timeout: 20_000 })
  await questionTab.click()
  await waitFor('question agent composer ready', async () => {
    return page.locator('[data-testid="composer-editor"]:visible .ProseMirror[contenteditable="true"]').first().isVisible().catch(() => false)
  }, 20_000)

  await typeComposerAndSend(
    page,
    'Help standardize our retry jitter policy across all internal HTTP clients.',
  )
  await waitFor('question control banner visible', async () => {
    const banner = page.locator('[data-testid="control-banner"]:visible').first()
    if (!await banner.isVisible().catch(() => false))
      return false
    const text = await banner.textContent()
    return Boolean(text && text.includes('Full jitter (Recommended)'))
  }, 30_000)
  await page.waitForTimeout(250)

  const bannerBox = await requireBox(page.locator('[data-testid="control-banner"]:visible').first(), 'question control banner')
  const questionTop = Math.max(0, bannerBox.y - 120)
  const questionClip = clampClip({
    x: centerClip.x,
    y: questionTop,
    width: centerClip.width,
    height: VIEWPORT.height - questionTop,
  })
  await snap(page, 'question-prompt-banner', 'light', questionClip, recorded)
  await waitForTheme(page, 'dark')
  await snap(page, 'question-prompt-banner', 'dark', questionClip, recorded)
  await waitForTheme(page, 'light')
}

async function captureMobileAndLoginViews(
  browser: Browser,
  authContext: BrowserContext,
  hubUrl: string,
  wsMain: string,
  claudeId: string,
  recorded: Record<string, ClipRect>,
): Promise<void> {
  // 1. Mobile layout (390x844 single-column shell).
  const mobileViewport = { width: 390, height: 844 }
  const mobilePage = await authContext.newPage()
  try {
    await mobilePage.setViewportSize(mobileViewport)
    await mobilePage.goto('/', { waitUntil: 'domcontentloaded' })
    await mobilePage.locator('[data-testid="composer-editor"]:visible').first().waitFor({ state: 'visible', timeout: 25_000 })
    // Ensure Retry jitter tab is shown if multiple tabs exist.
    const mobileTab = mobilePage.locator(`[data-testid="tab"][data-tab-id="${claudeId}"]:visible`).first()
    if (await mobileTab.isVisible().catch(() => false))
      await mobileTab.click()
    await waitForTheme(mobilePage, 'light')
    await snap(mobilePage, 'mobile-layout', 'light', { x: 0, y: 0, ...mobileViewport }, recorded)
    await waitForTheme(mobilePage, 'dark')
    await snap(mobilePage, 'mobile-layout', 'dark', { x: 0, y: 0, ...mobileViewport }, recorded)
  }
  finally {
    await mobilePage.close()
  }

  // 2. Login page (/login in a clean unauthenticated context).
  const loginViewport = { width: 960, height: 680 }
  const anonContext = await browser.newContext({
    baseURL: hubUrl,
    viewport: loginViewport,
    deviceScaleFactor: 2,
    colorScheme: 'light',
  })
  try {
    const loginPage = await anonContext.newPage()
    await loginPage.goto('/login', { waitUntil: 'domcontentloaded' })
    await loginPage.getByRole('button', { name: /Sign in/i }).first().waitFor({ state: 'visible', timeout: 20_000 })
    await waitForTheme(loginPage, 'light')
    const headingBox = await requireBox(loginPage.locator('h1').first(), 'login heading')
    const formBox = await requireBox(loginPage.locator('form').first(), 'login form')
    const loginUnion = unionRects(headingBox, formBox)
    const loginClip = clampClip({
      x: Math.max(0, loginUnion.x - 96),
      y: Math.max(0, loginUnion.y - 72),
      width: loginUnion.width + 192,
      height: loginUnion.height + 156,
    }, loginViewport)
    await snap(loginPage, 'login-page', 'light', loginClip, recorded)
    await waitForTheme(loginPage, 'dark')
    await snap(loginPage, 'login-page', 'dark', loginClip, recorded)
  }
  finally {
    await anonContext.close()
  }
  void wsMain
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
  const recordedClips: Record<string, ClipRect> = {}

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
        {
          name: 'input-queue-hold',
          priority: 'high',
          once: true,
          when: { user: 'Run the retry stress benchmark' },
          respond: {
            text: 'Completed the 16-goroutine retry stress benchmark with zero heap allocations.',
            delayMs: 45_000,
          },
        },
        {
          name: 'question-prompt-demo',
          priority: 'high',
          once: true,
          when: { user: 'Help standardize our retry jitter policy' },
          respond: {
            text: 'Before updating the shared HTTP client defaults, confirm which jitter strategy the services should adopt.',
            toolCalls: [
              askUserQuestionToolCall(AgentProvider.CLAUDE_CODE, 'call_ask_jitter', [
                {
                  header: 'Jitter',
                  question: 'Which retry jitter strategy should we standardize on across services?',
                  options: [
                    {
                      label: 'Full jitter (Recommended)',
                      description: 'Uniform random delay in [0, min(cap, base * 2^attempt)] for maximum spread.',
                    },
                    {
                      label: 'Equal jitter',
                      description: 'Keep half of the exponential delay deterministic and randomize the other half.',
                    },
                    {
                      label: 'Decorrelated jitter',
                      description: 'Compute each sleep from the previous sleep duration up to maxBackoff.',
                    },
                  ],
                },
              ]),
            ],
          },
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
      // Advertise a clean example Hub URL for RegisterWorkerDialog (the only
      // consumer of workerHubUrl) and supply a deterministic registration key
      // when that dialog opens during screenshot capture.
      await page.route('**/leapmux.v1.SystemService/GetSystemInfo', async (route) => {
        const response = await route.fetch()
        const body = await response.json() as Record<string, unknown>
        body.workerHubUrl = 'https://hub.example.com'
        await route.fulfill({ response, json: body })
      })
      await page.route('**/leapmux.v1.WorkerManagementService/CreateRegistrationKey', async (route) => {
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            registrationKey: 'lmx_reg_example_7f8a2c9e4b1d',
            expiresAt: new Date(Date.now() + 5 * 60 * 1000).toISOString(),
          }),
        })
      })
      await page.route('**/leapmux.v1.WorkerManagementService/RevokeRegistrationKey', async (route) => {
        await route.fulfill({ status: 200, contentType: 'application/json', body: '{}' })
      })

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

      console.log('Driving live Claude Code turn (Read -> Edit diff -> Bash permission prompt) ...')
      await typeComposerAndSend(
        page,
        'Add full jitter to CalculateBackoff in internal/retry/backoff.go so retrying clients do not synchronize retries, then run the retry package tests.',
      )

      // Approve the Edit tool call so Claude writes backoff.go and renders the real Edit diff card.
      const editAllowBtn = await waitForControlAllowButton(page, 45_000)
      await editAllowBtn.click()

      // Wait for the next permission prompt (the Bash `go test` call) and capture
      // coding-agent-chat while the agent tile spans the full center width.
      await waitFor('Bash permission prompt visible', async () => {
        const banner = page.locator('[data-testid="control-banner"]:visible').first()
        if (!await banner.isVisible().catch(() => false))
          return false
        const text = await banner.textContent()
        return Boolean(text && text.includes('go test'))
      }, 45_000)

      console.log('Capturing coding-agent-chat screenshots (light & dark) ...')
      const chatClip = await centerCanvasClip(page)
      await waitForTheme(page, 'light')
      await snap(page, 'coding-agent-chat', 'light', chatClip, recordedClips)
      await waitForTheme(page, 'dark')
      await snap(page, 'coding-agent-chat', 'dark', chatClip, recordedClips)
      await waitForTheme(page, 'light')

      // Approve the Bash command so Turn 1 completes cleanly.
      const bashAllowBtn = await waitForControlAllowButton(page, 15_000)
      await bashAllowBtn.click()
      await waitFor('Claude turn completed after Bash tests', async () => {
        const text = await page.locator('[data-testid="tile"]').first().textContent()
        return Boolean(text && text.includes('All retry package tests passed'))
      }, 45_000)

      console.log('Building 3-pane tiling layout (Agent | File Diff / Terminal) ...')
      const split1Saved = waitForLayoutSaved(page)
      await page.locator('[data-testid="split-vertical"]').first().click()
      await waitFor('2 tiles visible', async () => (await page.locator('[data-testid="tile"]').count()) === 2, 10_000)
      await split1Saved

      const tile1 = page.locator('[data-testid="tile"]').nth(1)
      const tile1Id = await tile1.getAttribute('data-tile-id')
      if (!tile1Id)
        throw new Error('Tile 1 has no data-tile-id attribute')

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
      await diffToolbar.getByText('Unified', { exact: true }).click()
      await waitFor('Unified diff lines rendered', async () => {
        const text = await tile1.textContent()
        return Boolean(text && text.includes('delay := baseBackoff') && text.includes('rand.N(ceiling + 1)'))
      }, 15_000)

      const split2Saved = waitForLayoutSaved(page)
      await page.locator('[data-testid="tile"]').nth(1).locator('[data-testid="split-horizontal"]').click()
      await waitFor('3 tiles visible', async () => (await page.locator('[data-testid="tile"]').count()) === 3, 10_000)
      await split2Saved

      const tile2 = page.locator('[data-testid="tile"]').nth(2)
      const tile2Id = await tile2.getAttribute('data-tile-id')
      if (!tile2Id)
        throw new Error('Tile 2 has no data-tile-id attribute')

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

      // Ensure Tile 1 (backoff.go) stays in Unified diff mode after split-horizontal.
      const tile1AfterSplit = page.locator('[data-testid="tile"]').nth(1)
      const diffToolbarAfterSplit = tile1AfterSplit.locator('[data-testid="diff-mode-toolbar"]:visible').first()
      await diffToolbarAfterSplit.waitFor({ state: 'visible', timeout: 15_000 })
      await diffToolbarAfterSplit.getByText('Unified', { exact: true }).click()
      await waitFor('Unified diff lines rendered after split', async () => {
        const text = await tile1AfterSplit.textContent()
        return Boolean(text && text.includes('delay := baseBackoff') && text.includes('rand.N(ceiling + 1)'))
      }, 15_000)

      // Focus Tile 0 and select the Changed git filter in the right File Browser.
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

      // Widen the left sidebar slightly so workspace titles and repo names fit
      // without horizontal clipping, then reset horizontal scroll to 0.
      const initialLeftHandle = await requireBox(page.locator('[data-testid="resize-handle"]').nth(0), 'initial left resize handle')
      await dragPointer(
        page,
        initialLeftHandle.x + initialLeftHandle.width / 2,
        VIEWPORT.height / 2,
        initialLeftHandle.x + initialLeftHandle.width / 2 + 44,
        VIEWPORT.height / 2,
      )
      await resetLeftSidebarScroll(page)
      await page.waitForTimeout(250)

      console.log('Capturing clean 3-pane workspace views (light & dark) ...')
      await captureCleanWorkspaceViews(page, 'light', wsOAuth, recordedClips)
      await captureCleanWorkspaceViews(page, 'dark', wsOAuth, recordedClips)
      await waitForTheme(page, 'light')

      console.log('Popping out semi-transparent floating window for hero-workspace & floating-window ...')
      await openFloatingLogWindow(page, channel, state.workerId, wsMain, tile2Id, repos.wtRetryJitter)
      await captureFloatingViews(page, 'light', recordedClips)
      await captureFloatingViews(page, 'dark', recordedClips)
      await waitForTheme(page, 'light')
      await closeFloatingLogWindow(page)

      console.log('Capturing Quake-mode terminal screenshots (light & dark) ...')
      await captureQuakeTerminalViews(page, recordedClips)

      console.log('Capturing Markdown side-by-side file viewer screenshots (light & dark) ...')
      await captureMarkdownViewerViews(
        page,
        channel,
        state.workerId,
        wsMain,
        repos.wtRetryJitter,
        repos.readmePath,
        recordedClips,
      )

      console.log('Capturing light mode dialogs, menus, popovers & Preferences ...')
      await captureDialogsAndMenus(page, 'light', wsMain, recordedClips)
      console.log('Capturing dark mode dialogs, menus, popovers & Preferences ...')
      await captureDialogsAndMenus(page, 'dark', wsMain, recordedClips)

      console.log('Capturing mobile layout & login page (light & dark) ...')
      await captureMobileAndLoginViews(browser, context, state.hubUrl, wsMain, claudeId, recordedClips)

      console.log('Capturing input queue, schedule popover & question banner (light & dark) ...')
      await captureInputQueueAndQuestionViews(page, server, wsMain, repos.wtRetryJitter, recordedClips)

      console.log('Summary of captured dimensions:')
      for (const [name, clip] of Object.entries(recordedClips))
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
