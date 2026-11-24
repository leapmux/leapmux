/**
 * Deterministic documentation screenshot generator for https://leapmux.dev.
 *
 * Renders realistic LeapMux workspace scenes in headless Chromium using the
 * repository's own design tokens (`contracts/theme-default.json`), bundled
 * Hack Nerd Font webfonts (`frontend/public/fonts/`), agent provider SVGs
 * (`icons/agents/`), and Shiki syntax highlighting (`github-light` /
 * `github-dark`).
 *
 * Run explicitly by developers when the UI or fixtures change:
 *   task generate-site-screenshots
 *
 * Outputs 12 Retina (2x) PNGs (6 views x light/dark) into
 * `site/static/images/docs/`.
 */

import { mkdirSync, readFileSync } from 'node:fs'
import { createServer } from 'node:http'
import { createRequire } from 'node:module'
import { extname, resolve } from 'node:path'
import process from 'node:process'

const ROOT = resolve(import.meta.dirname, '..')
const OUT_DIR = resolve(ROOT, 'site/static/images/docs')
const require = createRequire(new URL('../frontend/package.json', import.meta.url))
const { chromium } = require('playwright-core')
const { createHighlighter } = require('shiki')

const THEME_CONTRACT = JSON.parse(readFileSync(resolve(ROOT, 'contracts/theme-default.json'), 'utf8'))

const VIEWPORT = { width: 1440, height: 860 }

// Dimidium ANSI colors, matching frontend/src/styles/themes/default.ts.
const ANSI = {
  light: {
    red: '#b83d41',
    green: '#4d9833',
    yellow: '#ba8300',
    blue: '#0464ba',
    magenta: '#9c50bd',
    cyan: '#019a9f',
    muted: '#737575',
  },
  dark: {
    red: '#cf494c',
    green: '#60b442',
    yellow: '#db9c11',
    blue: '#0575d8',
    magenta: '#af5ed2',
    cyan: '#1db6bb',
    muted: '#817e7e',
  },
}

const MIME = {
  '.woff2': 'font/woff2',
  '.svg': 'image/svg+xml',
  '.css': 'text/css; charset=utf-8',
  '.html': 'text/html; charset=utf-8',
}

function escapeHtml(text) {
  return text
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
}

/** Inline small Lucide-style 16px stroke SVGs so zero external icon CDN is needed. */
const ICONS = {
  gitBranch: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="6" y1="3" x2="6" y2="15"/><circle cx="18" cy="6" r="3"/><circle cx="6" cy="18" r="3"/><path d="M18 9a9 9 0 0 1-9 9"/></svg>',
  folder: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z"/></svg>',
  fileCode: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z"/><path d="M14 2v4a2 2 0 0 0 2 2h4"/><path d="m10 13-2 2 2 2"/><path d="m14 17 2-2-2-2"/></svg>',
  terminal: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="4 17 10 11 4 5"/><line x1="12" y1="19" x2="20" y2="19"/></svg>',
  chevronDown: '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m6 9 6 6 6-6"/></svg>',
  chevronRight: '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m9 18 6-6-6-6"/></svg>',
  plus: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12h14"/><path d="M12 5v14"/></svg>',
  x: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>',
  splitCols: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect width="18" height="18" x="3" y="3" rx="2"/><line x1="12" y1="3" x2="12" y2="21"/></svg>',
  splitRows: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect width="18" height="18" x="3" y="3" rx="2"/><line x1="3" y1="12" x2="21" y2="12"/></svg>',
  server: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect width="20" height="8" x="2" y="2" rx="2" ry="2"/><rect width="20" height="8" x="2" y="14" rx="2" ry="2"/><line x1="6" y1="6" x2="6.01" y2="6"/><line x1="6" y1="18" x2="6.01" y2="18"/></svg>',
  lock: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect width="18" height="11" x="3" y="11" rx="2" ry="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/></svg>',
  shield: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z"/></svg>',
  send: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m22 2-7 20-4-9-9-4Z"/><path d="M22 2 11 13"/></svg>',
  check: '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg>',
  sparkles: '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M9.937 15.5A2 2 0 0 0 8.5 14.063l-6.135-1.582a.5.5 0 0 1 0-.962L8.5 9.936A2 2 0 0 0 9.937 8.5l1.582-6.135a.5.5 0 0 1 .963 0L14.063 8.5A2 2 0 0 0 15.5 9.937l6.135 1.581a.5.5 0 0 1 0 .964L15.5 14.063a2 2 0 0 0-1.437 1.437l-1.582 6.135a.5.5 0 0 1-.963 0z"/></svg>',
  search: '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/></svg>',
}

const DIFF_LINES = [
  {
    kind: 'ctx',
    oldNum: 42,
    newNum: 42,
    code: 'func (s *Store) Claim(ctx context.Context, key string, hash [32]byte) (*Record, error) {',
  },
  {
    kind: 'ctx',
    oldNum: 43,
    newNum: 43,
    code: '    tx, err := s.db.BeginTx(ctx, nil)',
  },
  {
    kind: 'ctx',
    oldNum: 44,
    newNum: 44,
    code: '    if err != nil {',
  },
  {
    kind: 'ctx',
    oldNum: 45,
    newNum: 45,
    code: '        return nil, fmt.Errorf("begin idempotency tx: %w", err)',
  },
  {
    kind: 'ctx',
    oldNum: 46,
    newNum: 46,
    code: '    }',
  },
  {
    kind: 'ctx',
    oldNum: 47,
    newNum: 47,
    code: '    defer tx.Rollback()',
  },
  {
    kind: 'del',
    oldNum: 48,
    newNum: null,
    code: '    rec, err := s.findLocked(ctx, tx, key)',
  },
  {
    kind: 'del',
    oldNum: 49,
    newNum: null,
    code: '    if err == nil {',
  },
  {
    kind: 'del',
    oldNum: 50,
    newNum: null,
    code: '        return rec, nil',
  },
  {
    kind: 'add',
    oldNum: null,
    newNum: 48,
    code: '    rec, err := s.findForUpdate(ctx, tx, key)',
  },
  {
    kind: 'add',
    oldNum: null,
    newNum: 49,
    code: '    if err == nil {',
  },
  {
    kind: 'add',
    oldNum: null,
    newNum: 50,
    code: '        if rec.RequestHash != hash {',
  },
  {
    kind: 'add',
    oldNum: null,
    newNum: 51,
    code: '            return nil, ErrKeyConflict',
  },
  {
    kind: 'add',
    oldNum: null,
    newNum: 52,
    code: '        }',
  },
  {
    kind: 'add',
    oldNum: null,
    newNum: 53,
    code: '        if rec.ExpiresAt.After(s.clock.Now()) {',
  },
  {
    kind: 'add',
    oldNum: null,
    newNum: 54,
    code: '            return rec, nil',
  },
  {
    kind: 'add',
    oldNum: null,
    newNum: 55,
    code: '        }',
  },
  {
    kind: 'ctx',
    oldNum: 51,
    newNum: 56,
    code: '    }',
  },
  {
    kind: 'ctx',
    oldNum: 52,
    newNum: 57,
    code: '    if err != nil && !errors.Is(err, sql.ErrNoRows) {',
  },
  {
    kind: 'ctx',
    oldNum: 53,
    newNum: 58,
    code: '        return nil, err',
  },
  {
    kind: 'ctx',
    oldNum: 54,
    newNum: 59,
    code: '    }',
  },
  {
    kind: 'add',
    oldNum: null,
    newNum: 60,
    code: '    next := &Record{Key: key, RequestHash: hash, ExpiresAt: s.clock.Now().Add(s.ttl)}',
  },
  {
    kind: 'add',
    oldNum: null,
    newNum: 61,
    code: '    if err := s.upsert(ctx, tx, next); err != nil {',
  },
  {
    kind: 'add',
    oldNum: null,
    newNum: 62,
    code: '        return nil, fmt.Errorf("upsert idempotency record: %w", err)',
  },
  {
    kind: 'add',
    oldNum: null,
    newNum: 63,
    code: '    }',
  },
  {
    kind: 'ctx',
    oldNum: 55,
    newNum: 64,
    code: '    return next, tx.Commit()',
  },
  {
    kind: 'ctx',
    oldNum: 56,
    newNum: 65,
    code: '}',
  },
]

const INLINE_EDIT_LINES = DIFF_LINES.slice(5, 18)

function renderHighlightedDiffRows(highlighter, themeName, rows) {
  const tokenLines = highlighter.codeToTokensBase(
    rows.map(r => r.code).join('\n'),
    { lang: 'go', theme: themeName },
  )
  return rows.map((row, idx) => {
    const tokens = tokenLines[idx] ?? []
    const html = tokens
      .map(tok => `<span style="color:${tok.color}">${escapeHtml(tok.content)}</span>`)
      .join('')
    const sign = row.kind === 'add'
      ? '+'
      : row.kind === 'del'
        ? '−'
        : ' '
    return `<div class="diff-row diff-${row.kind}"><span class="diff-ln">${row.oldNum ?? ''}</span><span class="diff-ln">${row.newNum ?? ''}</span><span class="diff-sign">${sign}</span><span class="diff-code">${html}</span></div>`
  }).join('')
}

function paletteCssVars(mode) {
  const tokens = THEME_CONTRACT[mode]
  return Object.entries(tokens)
    .map(([key, value]) => `  ${key}: ${value};`)
    .join('\n')
}

function buildStyles(mode) {
  const ansi = ANSI[mode]
  const diffAddBg = mode === 'dark'
    ? 'rgba(132, 204, 22, 0.14)'
    : 'rgba(101, 163, 13, 0.12)'
  const diffDelBg = mode === 'dark'
    ? 'rgba(239, 83, 80, 0.15)'
    : 'rgba(220, 74, 68, 0.11)'
  const codeBg = mode === 'dark'
    ? 'rgba(232, 230, 225, 0.06)'
    : 'rgba(34, 32, 30, 0.045)'
  const userBubbleBg = mode === 'dark'
    ? 'rgba(20, 184, 166, 0.12)'
    : 'rgba(13, 148, 136, 0.09)'
  const userBubbleBorder = mode === 'dark'
    ? 'rgba(20, 184, 166, 0.28)'
    : 'rgba(13, 148, 136, 0.24)'
  const queueBg = mode === 'dark'
    ? 'rgba(251, 191, 36, 0.08)'
    : 'rgba(245, 158, 11, 0.09)'
  const queueBorder = mode === 'dark'
    ? 'rgba(251, 191, 36, 0.30)'
    : 'rgba(245, 158, 11, 0.36)'

  return `
@font-face {
  font-family: 'Hack NF';
  src: url('/fonts/HackNerdFont-3.003-Regular.woff2') format('woff2');
  font-weight: 400;
  font-style: normal;
}
@font-face {
  font-family: 'Hack NF';
  src: url('/fonts/HackNerdFont-3.003-Bold.woff2') format('woff2');
  font-weight: 700;
  font-style: normal;
}

:root {
${paletteCssVars(mode)}
  --font-sans: -apple-system, BlinkMacSystemFont, "Segoe UI", Inter, Roboto, sans-serif;
  --font-mono: "Hack NF", ui-monospace, SFMono-Regular, Menlo, monospace;
  --radius-sm: 3px;
  --radius-md: 6px;
  --radius-lg: 10px;
  --diff-add: ${diffAddBg};
  --diff-del: ${diffDelBg};
  --code-bg: ${codeBg};
  --user-bubble-bg: ${userBubbleBg};
  --user-bubble-border: ${userBubbleBorder};
  --queue-bg: ${queueBg};
  --queue-border: ${queueBorder};
  --ansi-red: ${ansi.red};
  --ansi-green: ${ansi.green};
  --ansi-yellow: ${ansi.yellow};
  --ansi-blue: ${ansi.blue};
  --ansi-magenta: ${ansi.magenta};
  --ansi-cyan: ${ansi.cyan};
  --ansi-muted: ${ansi.muted};
}

* { box-sizing: border-box; margin: 0; padding: 0; }
html, body {
  width: ${VIEWPORT.width}px;
  height: ${VIEWPORT.height}px;
  overflow: hidden;
  background: var(--background);
  color: var(--foreground);
  font-family: var(--font-sans);
  font-size: 13px;
  line-height: 1.45;
  -webkit-font-smoothing: antialiased;
}

.app-shell {
  display: grid;
  grid-template-columns: 256px 1fr 272px;
  width: 100%;
  height: 100%;
  background: var(--background);
}

/* ---- Sidebars ---- */
.sidebar {
  display: flex;
  flex-direction: column;
  background: var(--card);
  overflow: hidden;
}
.sidebar-left { border-right: 1px solid var(--border); }
.sidebar-right { border-left: 1px solid var(--border); }

.sidebar-brand {
  height: 38px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 12px;
  border-bottom: 1px solid var(--border);
}
.brand-left {
  display: flex;
  align-items: center;
  gap: 8px;
  font-weight: 700;
  font-size: 13.5px;
  letter-spacing: -0.01em;
}
.brand-left img { width: 18px; height: 18px; }
.e2ee-pill {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 10.5px;
  font-weight: 600;
  padding: 2px 7px;
  border-radius: 999px;
  color: var(--primary);
  background: var(--accent);
}

.sb-section { display: flex; flex-direction: column; }
.sb-section-grow { flex: 1; min-height: 0; }
.sb-section + .sb-section { border-top: 1px solid var(--border); }
.sb-header {
  height: 30px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 10px;
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.04em;
  text-transform: uppercase;
  color: var(--muted-foreground);
}
.sb-header-left { display: flex; align-items: center; gap: 5px; }
.sb-icon-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  border-radius: var(--radius-sm);
  color: var(--muted-foreground);
}

.ws-list { padding: 4px 6px 8px; display: flex; flex-direction: column; gap: 2px; }
.ws-row {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 5px 8px;
  border-radius: var(--radius-md);
  font-weight: 600;
  color: var(--foreground);
}
.ws-row.active { background: var(--secondary); }
.ws-row .ws-path {
  margin-left: auto;
  font-size: 11px;
  font-weight: 400;
  color: var(--faint-foreground);
  font-family: var(--font-mono);
}

.branch-group {
  margin-left: 14px;
  margin-top: 2px;
  display: flex;
  flex-direction: column;
  gap: 1px;
}
.branch-row {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 8px;
  border-radius: var(--radius-sm);
  color: var(--foreground);
  font-size: 12px;
  font-weight: 600;
}
.branch-row .branch-icon { color: var(--primary); display: inline-flex; }
.wt-badge {
  font-size: 10px;
  font-weight: 600;
  padding: 1px 5px;
  border-radius: var(--radius-sm);
  background: var(--faint);
  border: 1px solid var(--border);
  color: var(--muted-foreground);
  font-family: var(--font-mono);
}
.dirty-badge {
  margin-left: auto;
  font-size: 10.5px;
  font-family: var(--font-mono);
  color: var(--warning);
  font-weight: 700;
}

.tab-leaf {
  margin-left: 18px;
  display: flex;
  align-items: center;
  gap: 7px;
  padding: 4px 8px;
  border-radius: var(--radius-sm);
  font-size: 12.2px;
  color: var(--muted-foreground);
}
.tab-leaf.active {
  background: var(--secondary);
  color: var(--foreground);
  font-weight: 600;
}
.tab-leaf img { width: 15px; height: 15px; flex-shrink: 0; }
.tab-leaf .leaf-title { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.status-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  margin-left: auto;
  flex-shrink: 0;
}
.dot-working { background: var(--primary); box-shadow: 0 0 0 2px var(--accent); }
.dot-waiting { background: var(--warning); box-shadow: 0 0 0 2px var(--lm-warning-subtle); }
.dot-ready { background: var(--success); }
.dot-idle { background: var(--faint-foreground); opacity: 0.6; }

.worker-list { padding: 4px 8px 10px; display: flex; flex-direction: column; gap: 5px; }
.worker-card {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 8px;
  border-radius: var(--radius-md);
  background: var(--faint);
  border: 1px solid var(--border);
}
.worker-meta { display: flex; flex-direction: column; min-width: 0; }
.worker-name { font-size: 12px; font-weight: 600; color: var(--foreground); }
.worker-sub { font-size: 10.5px; color: var(--muted-foreground); font-family: var(--font-mono); }
.worker-tag {
  margin-left: auto;
  font-size: 10px;
  font-weight: 600;
  padding: 1px 6px;
  border-radius: 999px;
  background: var(--lm-success-subtle);
  color: var(--success);
}

/* ---- Center Tiling Area ---- */
.center-grid {
  display: grid;
  grid-template-columns: 1.12fr 0.88fr;
  height: 100%;
  min-width: 0;
  background: var(--background);
}
.center-grid.scene-diff {
  grid-template-columns: 0.92fr 1.08fr;
}
.right-col-split {
  display: grid;
  grid-template-rows: 1.15fr 0.85fr;
  height: 100%;
  min-height: 0;
  border-left: 1px solid var(--border);
}
.right-col-split.scene-diff-split {
  grid-template-rows: 1.38fr 0.62fr;
}
.tile {
  display: flex;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
  overflow: hidden;
  background: var(--background);
}
.tile + .tile { border-top: 1px solid var(--border); }

.tab-bar {
  height: 38px;
  display: flex;
  align-items: stretch;
  justify-content: space-between;
  padding: 0 6px 0 8px;
  background: var(--background);
  border-bottom: 1px solid var(--border);
  flex-shrink: 0;
}
.tile.focused > .tab-bar { border-bottom: 1.5px solid var(--primary); }
.tabs-strip { display: flex; align-items: stretch; gap: 2px; min-width: 0; overflow: hidden; }
.tab-item {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 0 9px;
  font-size: 12px;
  color: var(--muted-foreground);
  border-bottom: 2px solid transparent;
  margin-bottom: -1px;
  white-space: nowrap;
  flex-shrink: 0;
}
.tab-item.active {
  color: var(--foreground);
  font-weight: 600;
  border-bottom-color: var(--primary);
}
.tab-item img { width: 15px; height: 15px; }
.tab-item .tab-x { color: var(--faint-foreground); display: inline-flex; margin-left: 2px; }
.tab-actions {
  display: flex;
  align-items: center;
  gap: 4px;
  padding-left: 8px;
  color: var(--muted-foreground);
}
.tab-action-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  border-radius: var(--radius-sm);
}

/* ---- Chat Pane ---- */
.chat-body {
  flex: 1;
  min-height: 0;
  padding: 14px 16px 10px;
  display: flex;
  flex-direction: column;
  gap: 11px;
  overflow: hidden;
}
.msg-user {
  align-self: flex-end;
  max-width: 88%;
  background: var(--user-bubble-bg);
  border: 1px solid var(--user-bubble-border);
  border-radius: var(--radius-lg);
  padding: 9px 13px;
  font-size: 12.6px;
  color: var(--foreground);
}
.msg-assistant {
  display: flex;
  flex-direction: column;
  gap: 8px;
  font-size: 12.6px;
}
.msg-assistant p { color: var(--foreground); line-height: 1.5; }
.msg-assistant code {
  font-family: var(--font-mono);
  font-size: 11.5px;
  background: var(--code-bg);
  padding: 1px 5px;
  border-radius: var(--radius-sm);
}

.tool-card {
  border: 1px solid var(--border);
  border-radius: var(--radius-md);
  background: var(--card);
  overflow: hidden;
}
.tool-card-hdr {
  display: flex;
  align-items: center;
  gap: 7px;
  padding: 6px 10px;
  border-bottom: 1px solid var(--border);
  font-size: 11.8px;
  font-weight: 600;
}
.tool-pill {
  font-size: 10.5px;
  font-family: var(--font-mono);
  padding: 1px 6px;
  border-radius: var(--radius-sm);
  background: var(--secondary);
  color: var(--foreground);
}
.tool-path { font-family: var(--font-mono); font-size: 11.5px; color: var(--muted-foreground); }
.tool-stat { margin-left: auto; font-family: var(--font-mono); font-size: 11px; display: flex; gap: 6px; }
.stat-add { color: var(--success); font-weight: 700; }
.stat-del { color: var(--danger); font-weight: 700; }

/* ---- Diff Rows ---- */
.diff-table {
  font-family: var(--font-mono);
  font-size: 11.2px;
  line-height: 1.48;
  background: var(--background);
}
.diff-row {
  display: grid;
  grid-template-columns: 32px 32px 16px 1fr;
  padding: 0 8px 0 4px;
  white-space: pre;
}
.diff-add { background: var(--diff-add); }
.diff-del { background: var(--diff-del); }
.diff-ln {
  text-align: right;
  padding-right: 6px;
  color: var(--faint-foreground);
  user-select: none;
  font-size: 10.5px;
}
.diff-sign { text-align: center; font-weight: 700; color: var(--faint-foreground); }
.diff-add .diff-sign { color: var(--success); }
.diff-del .diff-sign { color: var(--danger); }
.diff-code { overflow: hidden; text-overflow: ellipsis; }

/* ---- Permission Card ---- */
.perm-card {
  border: 1px solid var(--warning);
  background: var(--lm-warning-subtle);
  border-radius: var(--radius-md);
  padding: 9px 11px;
  display: flex;
  flex-direction: column;
  gap: 7px;
}
.perm-hdr {
  display: flex;
  align-items: center;
  gap: 7px;
  font-size: 12px;
  font-weight: 700;
  color: var(--foreground);
}
.perm-hdr svg { color: var(--warning); }
.perm-cmd {
  font-family: var(--font-mono);
  font-size: 11.4px;
  background: var(--background);
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  padding: 5px 8px;
  color: var(--foreground);
}
.perm-actions { display: flex; gap: 6px; align-items: center; }
.btn {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 11.8px;
  font-weight: 600;
  padding: 4px 10px;
  border-radius: var(--radius-md);
  border: 1px solid var(--border);
  background: var(--background);
  color: var(--foreground);
}
.btn-primary {
  background: var(--primary);
  border-color: var(--primary);
  color: var(--primary-foreground);
}
.btn-danger-outline {
  color: var(--danger);
  border-color: var(--border);
}

/* ---- Queued Input Strip + Composer ---- */
.queue-strip {
  margin: 0 14px 6px;
  padding: 6px 10px;
  border-radius: var(--radius-md);
  background: var(--queue-bg);
  border: 1px solid var(--queue-border);
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 11.8px;
}
.queue-tag {
  font-size: 10px;
  font-weight: 700;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: var(--warning);
}
.queue-text {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--foreground);
}
.steer-btn {
  font-size: 11px;
  font-weight: 700;
  padding: 2px 8px;
  border-radius: var(--radius-sm);
  background: var(--primary);
  color: var(--primary-foreground);
}

.composer-wrap {
  padding: 0 14px 10px;
  display: flex;
  flex-direction: column;
  gap: 6px;
  flex-shrink: 0;
}
.composer-box {
  border: 1px solid var(--input);
  border-radius: var(--radius-md);
  background: var(--card);
  padding: 8px 10px;
  display: flex;
  align-items: center;
  gap: 8px;
}
.composer-plus {
  width: 24px;
  height: 24px;
  border-radius: var(--radius-sm);
  border: 1px solid var(--border);
  background: var(--background);
  display: inline-flex;
  align-items: center;
  justify-content: center;
  color: var(--muted-foreground);
}
.composer-placeholder {
  flex: 1;
  color: var(--faint-foreground);
  font-size: 12.5px;
}
.composer-send {
  width: 26px;
  height: 26px;
  border-radius: var(--radius-md);
  background: var(--primary);
  color: var(--primary-foreground);
  display: inline-flex;
  align-items: center;
  justify-content: center;
}
.status-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 6px;
  padding: 0 2px;
  min-width: 0;
}
.status-chips { display: flex; align-items: center; gap: 5px; flex-wrap: nowrap; min-width: 0; overflow: hidden; }
.chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 10.8px;
  color: var(--muted-foreground);
  padding: 2px 6px;
  border-radius: var(--radius-sm);
  background: var(--faint);
  border: 1px solid var(--border);
  white-space: nowrap;
  flex-shrink: 0;
}
.chip-mono { font-family: var(--font-mono); font-size: 10.4px; }
.ctx-meter { font-family: var(--font-mono); font-size: 10.6px; color: var(--muted-foreground); white-space: nowrap; flex-shrink: 0; }

/* ---- Codex Review Pane ---- */
.review-list { display: flex; flex-direction: column; gap: 7px; }
.finding {
  border-left: 3px solid var(--primary);
  background: var(--card);
  border-radius: 0 var(--radius-sm) var(--radius-sm) 0;
  padding: 7px 10px;
  font-size: 12px;
}
.finding-warn { border-left-color: var(--warning); }
.finding-title { font-weight: 700; margin-bottom: 2px; }
.finding-loc { font-family: var(--font-mono); font-size: 11px; color: var(--muted-foreground); }

/* ---- Terminal Pane ---- */
.term-body {
  flex: 1;
  min-height: 0;
  background: var(--background);
  padding: 10px 14px;
  font-family: var(--font-mono);
  font-size: 11.7px;
  line-height: 1.5;
  overflow: hidden;
  white-space: pre;
}
.t-prompt { color: var(--primary); font-weight: 700; }
.t-dir { color: var(--ansi-blue); font-weight: 700; }
.t-branch { color: var(--ansi-magenta); }
.t-ok { color: var(--ansi-green); font-weight: 700; }
.t-warn { color: var(--ansi-yellow); }
.t-muted { color: var(--ansi-muted); }
.t-cyan { color: var(--ansi-cyan); }
.t-cursor {
  display: inline-block;
  width: 7px;
  height: 13px;
  vertical-align: -2px;
  background: var(--primary);
}

/* ---- File Viewer Diff Pane ---- */
.fv-toolbar {
  height: 34px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 12px;
  background: var(--card);
  border-bottom: 1px solid var(--border);
  font-size: 11.8px;
  flex-shrink: 0;
}
.fv-path { font-family: var(--font-mono); font-weight: 600; color: var(--foreground); }
.fv-right { display: flex; align-items: center; gap: 8px; }
.pill-group {
  display: inline-flex;
  background: var(--faint);
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  padding: 1px;
}
.pill-opt {
  padding: 2px 8px;
  font-size: 11px;
  font-weight: 600;
  color: var(--muted-foreground);
  border-radius: 2px;
}
.pill-opt.active { background: var(--background); color: var(--foreground); }
.fv-diff-scroll { flex: 1; min-height: 0; overflow: hidden; padding-top: 4px; }

/* ---- Right Sidebar (Files + Goals) ---- */
.git-tabs {
  display: flex;
  gap: 2px;
  padding: 6px 8px 4px;
  border-bottom: 1px solid var(--border);
}
.git-tab {
  font-size: 11px;
  font-weight: 600;
  padding: 3px 8px;
  border-radius: var(--radius-sm);
  color: var(--muted-foreground);
}
.git-tab.active { background: var(--secondary); color: var(--foreground); }
.git-count {
  margin-left: 3px;
  font-family: var(--font-mono);
  font-size: 10px;
  color: var(--primary);
}
.file-tree { padding: 6px 6px 10px; display: flex; flex-direction: column; gap: 1px; }
.ft-row {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 8px;
  border-radius: var(--radius-sm);
  font-size: 12px;
  color: var(--muted-foreground);
}
.ft-row.active { background: var(--secondary); color: var(--foreground); font-weight: 600; }
.ft-row.dir { color: var(--foreground); font-weight: 600; }
.ft-indent-1 { padding-left: 20px; }
.ft-indent-2 { padding-left: 34px; }
.git-badge {
  margin-left: auto;
  font-family: var(--font-mono);
  font-size: 10.5px;
  font-weight: 700;
}
.git-m { color: var(--warning); }
.git-a { color: var(--success); }

.goals-box { padding: 8px 10px 12px; display: flex; flex-direction: column; gap: 6px; }
.todo-row { display: flex; align-items: flex-start; gap: 7px; font-size: 11.8px; color: var(--muted-foreground); }
.todo-check {
  width: 15px;
  height: 15px;
  margin-top: 1px;
  border-radius: 3px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  border: 1px solid var(--border);
}
.todo-check.done { background: var(--lm-success-subtle); border-color: var(--success); color: var(--success); }
.todo-check.active { background: var(--accent); border-color: var(--primary); color: var(--primary); }
.todo-done { text-decoration: line-through; opacity: 0.72; }
`
}

function renderLeftSidebar() {
  return `
  <aside class="sidebar sidebar-left" data-target="sidebar-left">
    <div class="sidebar-brand">
      <div class="brand-left">
        <img src="/logo.svg" alt="">
        <span>LeapMux</span>
      </div>
      <span class="e2ee-pill">${ICONS.lock} E2EE</span>
    </div>

    <div class="sb-section sb-section-grow">
      <div class="sb-header">
        <span class="sb-header-left">${ICONS.chevronDown} Workspaces</span>
        <span class="sb-icon-btn">${ICONS.plus}</span>
      </div>
      <div class="ws-list">
        <div class="ws-row active">
          ${ICONS.chevronDown}
          <span>payments-api</span>
          <span class="ws-path">go</span>
        </div>
        <div class="branch-group">
          <div class="branch-row">
            <span class="branch-icon">${ICONS.gitBranch}</span>
            <span>feat/stripe-idempotency</span>
            <span class="dirty-badge">+3</span>
          </div>
          <div class="tab-leaf active">
            <img src="/agents/claude-code.svg" alt="">
            <span class="leaf-title">Claude Code — Store</span>
            <span class="status-dot dot-waiting"></span>
          </div>
          <div class="tab-leaf">
            <img src="/agents/codex.svg" alt="">
            <span class="leaf-title">Codex — Second Opinion</span>
            <span class="status-dot dot-ready"></span>
          </div>
          <div class="tab-leaf">
            ${ICONS.terminal}
            <span class="leaf-title">zsh — go test</span>
            <span class="status-dot dot-ready"></span>
          </div>

          <div class="branch-row" style="margin-top:4px">
            <span class="branch-icon">${ICONS.gitBranch}</span>
            <span>fix/webhook-retry</span>
            <span class="wt-badge">wt</span>
          </div>
          <div class="tab-leaf">
            <img src="/agents/gemini-cli.svg" alt="">
            <span class="leaf-title">Gemini CLI — Backoff</span>
            <span class="status-dot dot-working"></span>
          </div>

          <div class="branch-row" style="margin-top:4px">
            <span class="branch-icon">${ICONS.gitBranch}</span>
            <span>test/refund-edges</span>
            <span class="wt-badge">wt</span>
          </div>
          <div class="tab-leaf">
            <img src="/agents/opencode.svg" alt="">
            <span class="leaf-title">OpenCode — Fuzz</span>
            <span class="status-dot dot-idle"></span>
          </div>
        </div>

        <div class="ws-row" style="margin-top:6px">
          ${ICONS.chevronRight}
          <span>billing-web</span>
          <span class="ws-path">ts</span>
        </div>
        <div class="ws-row">
          ${ICONS.chevronRight}
          <span>infra-deploy</span>
          <span class="ws-path">tf</span>
        </div>
      </div>
    </div>

    <div class="sb-section">
      <div class="sb-header">
        <span class="sb-header-left">${ICONS.chevronDown} Workers</span>
        <span class="sb-icon-btn">${ICONS.plus}</span>
      </div>
      <div class="worker-list">
        <div class="worker-card">
          ${ICONS.server}
          <div class="worker-meta">
            <span class="worker-name">macbook-pro</span>
            <span class="worker-sub">local · darwin/arm64</span>
          </div>
          <span class="worker-tag">Online</span>
        </div>
        <div class="worker-card">
          ${ICONS.server}
          <div class="worker-meta">
            <span class="worker-name">gpu-devbox</span>
            <span class="worker-sub">outbound · linux/amd64</span>
          </div>
          <span class="worker-tag">Online</span>
        </div>
      </div>
    </div>
  </aside>`
}

function renderClaudePane(inlineDiffHtml, compact = false) {
  return `
  <section class="tile focused" data-target="claude-pane">
    <div class="tab-bar">
      <div class="tabs-strip">
        <div class="tab-item active">
          <img src="/agents/claude-code.svg" alt="">
          <span>Claude Code — Store</span>
          <span class="tab-x">${ICONS.x}</span>
        </div>
        <div class="tab-item">
          <img src="/agents/gemini-cli.svg" alt="">
          <span>Gemini CLI</span>
        </div>
      </div>
      <div class="tab-actions">
        <span class="tab-action-btn">${ICONS.plus}</span>
        <span class="tab-action-btn">${ICONS.splitCols}</span>
        <span class="tab-action-btn">${ICONS.splitRows}</span>
      </div>
    </div>

    <div class="chat-body">
      <div class="msg-user">
        Harden <code>idempotency.Store.Claim</code> so concurrent retries reject payload mismatches with <code>ErrKeyConflict</code> and re-claim expired keys atomically.
      </div>

      <div class="msg-assistant">
        <p>I updated <code>Claim</code> to lock the existing row with <code>findForUpdate</code>, verify the SHA-256 request hash before returning a cached record, and overwrite expired entries inside the same transaction.</p>

        <div class="tool-card">
          <div class="tool-card-hdr">
            <span class="tool-pill">Edit</span>
            <span class="tool-path">internal/idempotency/store.go</span>
            <span class="tool-stat"><span class="stat-add">+12</span><span class="stat-del">−3</span></span>
          </div>
          <div class="diff-table">${inlineDiffHtml}</div>
        </div>

        ${compact
          ? ''
          : `<div class="perm-card">
          <div class="perm-hdr">${ICONS.shield} Permission required — Run shell command</div>
          <div class="perm-cmd">go test -race -count=1 ./internal/idempotency/...</div>
          <div class="perm-actions">
            <span class="btn btn-primary">${ICONS.check} Allow once</span>
            <span class="btn">Allow for session</span>
            <span class="btn btn-danger-outline">Deny</span>
          </div>
        </div>`}
      </div>
    </div>

    ${compact
      ? ''
      : `<div class="queue-strip">
      <span class="queue-tag">Queued · 1</span>
      <span class="queue-text">Also add a concurrent Claim race test using two goroutines with mismatched payload hashes.</span>
      <span class="steer-btn">Steer</span>
    </div>`}

    <div class="composer-wrap">
      <div class="composer-box">
        <span class="composer-plus">${ICONS.plus}</span>
        <span class="composer-placeholder">Message Claude Code… (⌘↵ to queue, ⇧⌘↵ to steer)</span>
        <span class="composer-send">${ICONS.send}</span>
      </div>
      <div class="status-bar">
        <div class="status-chips">
          <span class="chip chip-mono">${ICONS.gitBranch} feat/stripe-idempotency</span>
          <span class="chip">Claude Opus 4.6</span>
          <span class="chip">High</span>
          ${compact ? '' : '<span class="chip">Ask</span>'}
        </div>
        <span class="ctx-meter">42k / 200k</span>
      </div>
    </div>
  </section>`
}

function renderCodexPane() {
  return `
  <section class="tile">
    <div class="tab-bar">
      <div class="tabs-strip">
        <div class="tab-item active">
          <img src="/agents/codex.svg" alt="">
          <span>Codex — Second Opinion</span>
          <span class="tab-x">${ICONS.x}</span>
        </div>
      </div>
      <div class="tab-actions">
        <span class="tab-action-btn">${ICONS.splitRows}</span>
      </div>
    </div>
    <div class="chat-body">
      <div class="msg-user">
        Review <code>feat/stripe-idempotency</code> read-only for race conditions and SQLite/Postgres parity.
      </div>
      <div class="msg-assistant">
        <p><strong>Verdict:</strong> 2 findings before merge (contrasting read-only pass):</p>
        <div class="review-list">
          <div class="finding finding-warn">
            <div class="finding-title">1. SQLite lacks row-level SELECT FOR UPDATE</div>
            <div class="finding-loc">internal/idempotency/store.go:48 — use BEGIN IMMEDIATE on SQLite so concurrent Claim calls serialize without SQLITE_BUSY upgrades.</div>
          </div>
          <div class="finding">
            <div class="finding-title">2. Zero-TTL edge case in upsert</div>
            <div class="finding-loc">internal/idempotency/store.go:60 — guard s.ttl &lt;= 0 in NewStore to avoid immediate re-claim loops.</div>
          </div>
        </div>
      </div>
    </div>
  </section>`
}

function renderFileViewerDiffPane(fullDiffHtml) {
  return `
  <section class="tile focused" data-target="file-viewer">
    <div class="tab-bar">
      <div class="tabs-strip">
        <div class="tab-item active">
          ${ICONS.fileCode}
          <span>store.go</span>
          <span class="tab-x">${ICONS.x}</span>
        </div>
        <div class="tab-item">
          ${ICONS.fileCode}
          <span>store_test.go</span>
        </div>
      </div>
      <div class="tab-actions">
        <span class="tab-action-btn">${ICONS.splitCols}</span>
        <span class="tab-action-btn">${ICONS.splitRows}</span>
      </div>
    </div>
    <div class="fv-toolbar">
      <span class="fv-path">internal/idempotency/store.go</span>
      <div class="fv-right">
        <span class="tool-stat"><span class="stat-add">+12</span><span class="stat-del">−3</span></span>
        <div class="pill-group">
          <span class="pill-opt active">Diff</span>
          <span class="pill-opt">Source</span>
        </div>
      </div>
    </div>
    <div class="fv-diff-scroll">
      <div class="diff-table">${fullDiffHtml}</div>
    </div>
  </section>`
}

function renderTerminalPane() {
  return `
  <section class="tile" data-target="terminal-pane">
    <div class="tab-bar">
      <div class="tabs-strip">
        <div class="tab-item active">
          ${ICONS.terminal}
          <span>zsh — go test</span>
          <span class="tab-x">${ICONS.x}</span>
        </div>
        <div class="tab-item">
          ${ICONS.terminal}
          <span>stripe-listen</span>
        </div>
      </div>
      <div class="tab-actions">
        <span class="tab-action-btn">${ICONS.plus}</span>
      </div>
    </div>
    <div class="term-body"><span class="t-dir">~/src/payments-api</span> <span class="t-branch">(feat/stripe-idempotency)</span>
<span class="t-prompt">❯</span> leapmux tree --compact
<span class="t-cyan">payments-api</span>  <span class="t-muted">[macbook-pro]</span>
  ├─ <span class="t-branch">feat/stripe-idempotency</span>
  │   ├─ <span class="t-warn">waiting</span>  ag_01JM8K  Claude Code — Store
  │   ├─ <span class="t-ok">idle   </span>  ag_01JM9Q  Codex — Second Opinion
  │   └─ <span class="t-ok">ready  </span>  tm_01JM7P  zsh — go test
  └─ <span class="t-branch">fix/webhook-retry</span> <span class="t-muted">(worktree)</span>
      └─ <span class="t-cyan">working</span>  ag_01JMA4  Gemini CLI — Backoff

<span class="t-prompt">❯</span> go test -race -count=1 ./internal/idempotency/...
<span class="t-ok">ok</span>  github.com/acme/pay/idempotency  1.482s

<span class="t-prompt">❯</span> <span class="t-cursor"></span></div>
  </section>`
}

function renderRightSidebar(activeChanged = false) {
  return `
  <aside class="sidebar sidebar-right" data-target="sidebar-right">
    <div class="sb-section sb-section-grow">
      <div class="sb-header">
        <span class="sb-header-left">${ICONS.chevronDown} Files</span>
        <span class="sb-icon-btn">${ICONS.search}</span>
      </div>
      <div class="git-tabs">
        <span class="git-tab${activeChanged ? '' : ' active'}">All</span>
        <span class="git-tab${activeChanged ? ' active' : ''}">Changed <span class="git-count">4</span></span>
        <span class="git-tab">Staged</span>
      </div>
      <div class="file-tree">
        <div class="ft-row dir">${ICONS.chevronDown} ${ICONS.folder} internal</div>
        <div class="ft-row dir ft-indent-1">${ICONS.chevronDown} ${ICONS.folder} idempotency</div>
        <div class="ft-row active ft-indent-2">${ICONS.fileCode} store.go <span class="git-badge git-m">M</span></div>
        <div class="ft-row ft-indent-2">${ICONS.fileCode} store_test.go <span class="git-badge git-a">A</span></div>
        <div class="ft-row ft-indent-2">${ICONS.fileCode} errors.go <span class="git-badge git-m">M</span></div>
        <div class="ft-row dir ft-indent-1">${ICONS.chevronDown} ${ICONS.folder} webhook</div>
        <div class="ft-row ft-indent-2">${ICONS.fileCode} dispatcher.go <span class="git-badge git-m">M</span></div>
        <div class="ft-row dir">${ICONS.chevronRight} ${ICONS.folder} cmd</div>
        <div class="ft-row">${ICONS.fileCode} go.mod</div>
      </div>
    </div>

    <div class="sb-section">
      <div class="sb-header">
        <span class="sb-header-left">${ICONS.chevronDown} Goals &amp; To-dos</span>
        <span class="sb-icon-btn">${ICONS.sparkles}</span>
      </div>
      <div class="goals-box">
        <div class="todo-row">
          <span class="todo-check done">${ICONS.check}</span>
          <span class="todo-done">Lock existing key with findForUpdate</span>
        </div>
        <div class="todo-row">
          <span class="todo-check done">${ICONS.check}</span>
          <span class="todo-done">Reject payload hash mismatch with ErrKeyConflict</span>
        </div>
        <div class="todo-row">
          <span class="todo-check active">•</span>
          <span>Serialize SQLite Claim with BEGIN IMMEDIATE</span>
        </div>
        <div class="todo-row">
          <span class="todo-check"></span>
          <span>Run race suite &amp; push PR</span>
        </div>
      </div>
    </div>
  </aside>`
}

function buildHtml(scene, mode, highlighter) {
  const shikiTheme = mode === 'dark'
    ? 'github-dark'
    : 'github-light'
  const inlineDiffHtml = renderHighlightedDiffRows(highlighter, shikiTheme, INLINE_EDIT_LINES)
  const fullDiffHtml = renderHighlightedDiffRows(highlighter, shikiTheme, DIFF_LINES)
  const isDiffScene = scene === 'diff-and-layout'

  const centerHtml = isDiffScene
    ? `<main class="center-grid scene-diff" data-target="center-grid">
        ${renderClaudePane(inlineDiffHtml, true)}
        <div class="right-col-split scene-diff-split">
          ${renderFileViewerDiffPane(fullDiffHtml)}
          ${renderTerminalPane()}
        </div>
      </main>`
    : `<main class="center-grid" data-target="center-grid">
        ${renderClaudePane(inlineDiffHtml, false)}
        <div class="right-col-split">
          ${renderCodexPane()}
          ${renderTerminalPane()}
        </div>
      </main>`

  return `<!DOCTYPE html>
<html lang="en" data-theme="${mode}">
<head>
<meta charset="utf-8">
<style>${buildStyles(mode)}</style>
</head>
<body>
  <div class="app-shell">
    ${renderLeftSidebar()}
    ${centerHtml}
    ${renderRightSidebar(isDiffScene)}
  </div>
</body>
</html>`
}

/** Start an offline local static server for bundled webfonts, logo, and agent SVGs. */
function startAssetServer(pages) {
  return new Promise((resolveServer) => {
    const server = createServer((req, res) => {
      const urlPath = (req.url || '/').split('?')[0]
      if (pages.has(urlPath)) {
        res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' })
        res.end(pages.get(urlPath))
        return
      }
      let filePath = null
      if (urlPath === '/logo.svg')
        filePath = resolve(ROOT, 'site/static/images/logo.svg')
      else if (urlPath.startsWith('/fonts/'))
        filePath = resolve(ROOT, 'frontend/public/fonts', urlPath.slice('/fonts/'.length))
      else if (urlPath.startsWith('/agents/'))
        filePath = resolve(ROOT, 'icons/agents', urlPath.slice('/agents/'.length))

      if (filePath) {
        try {
          const data = readFileSync(filePath)
          res.writeHead(200, { 'Content-Type': MIME[extname(filePath)] || 'application/octet-stream' })
          res.end(data)
          return
        }
        catch {
          // Fall through to 404.
        }
      }
      res.writeHead(404)
      res.end('Not found')
    })
    server.listen(0, '127.0.0.1', () => {
      const addr = server.address()
      resolveServer({ server, baseUrl: `http://127.0.0.1:${addr.port}` })
    })
  })
}

/**
 * Screenshot definitions. Each cropped shot computes its bounds from the live
 * DOM elements via `boundingBox()` and adds generous surrounding context
 * (`pad`) clamped to the viewport so target borders and text never clip.
 */
const SHOTS = [
  { name: 'hero-workspace', scene: 'workspace', targets: null },
  {
    name: 'sidebar-workspaces',
    scene: 'workspace',
    targets: ['[data-target="sidebar-left"]'],
    pad: {
      left: 0,
      top: 0,
      right: 164,
      bottom: 0,
    },
  },
  {
    name: 'coding-agent-chat',
    scene: 'workspace',
    targets: ['[data-target="claude-pane"]'],
    pad: {
      left: 48,
      top: 0,
      right: 64,
      bottom: 0,
    },
  },
  {
    name: 'terminal-pane',
    scene: 'workspace',
    targets: ['[data-target="terminal-pane"]'],
    pad: {
      left: 56,
      top: 68,
      right: 72,
      bottom: 0,
    },
  },
  {
    name: 'tabs-tiling-layout',
    scene: 'diff-and-layout',
    targets: ['[data-target="center-grid"]'],
    pad: {
      left: 56,
      top: 0,
      right: 56,
      bottom: 0,
    },
  },
  {
    name: 'file-browser-diff',
    scene: 'diff-and-layout',
    targets: ['[data-target="file-viewer"]', '[data-target="sidebar-right"]'],
    pad: {
      left: 56,
      top: 0,
      right: 0,
      bottom: 0,
    },
    maxHeight: 620,
  },
]

async function resolveClip(page, shot) {
  if (!shot.targets)
    return null
  let minX = Infinity
  let minY = Infinity
  let maxX = -Infinity
  let maxY = -Infinity
  for (const selector of shot.targets) {
    const box = await page.locator(selector).boundingBox()
    if (!box)
      throw new Error(`Screenshot target not found: ${selector}`)
    minX = Math.min(minX, box.x)
    minY = Math.min(minY, box.y)
    maxX = Math.max(maxX, box.x + box.width)
    maxY = Math.max(maxY, box.y + box.height)
  }
  const pad = shot.pad ?? { left: 0, top: 0, right: 0, bottom: 0 }
  const x = Math.max(0, Math.floor(minX - pad.left))
  const y = Math.max(0, Math.floor(minY - pad.top))
  const right = Math.min(VIEWPORT.width, Math.ceil(maxX + pad.right))
  const bottomLimit = shot.maxHeight
    ? Math.min(VIEWPORT.height, y + shot.maxHeight)
    : VIEWPORT.height
  const bottom = Math.min(bottomLimit, Math.ceil(maxY + pad.bottom))
  return {
    x,
    y,
    width: right - x,
    height: bottom - y,
  }
}

async function main() {
  mkdirSync(OUT_DIR, { recursive: true })
  const highlighter = await createHighlighter({
    themes: ['github-light', 'github-dark'],
    langs: ['go'],
  })

  const pages = new Map()
  for (const scene of ['workspace', 'diff-and-layout']) {
    for (const mode of ['light', 'dark']) {
      pages.set(`/${scene}/${mode}`, buildHtml(scene, mode, highlighter))
    }
  }

  const { server, baseUrl } = await startAssetServer(pages)
  const browser = await chromium.launch()
  try {
    const context = await browser.newContext({
      viewport: VIEWPORT,
      deviceScaleFactor: 2,
    })
    const page = await context.newPage()

    for (const mode of ['light', 'dark']) {
      let loadedScene = null
      for (const shot of SHOTS) {
        if (loadedScene !== shot.scene) {
          await page.goto(`${baseUrl}/${shot.scene}/${mode}`, { waitUntil: 'networkidle' })
          await page.evaluate(() => document.fonts.ready)
          loadedScene = shot.scene
        }
        const clip = await resolveClip(page, shot)
        const outPath = resolve(OUT_DIR, `${shot.name}-${mode}.png`)
        await page.screenshot({
          path: outPath,
          ...(clip
            ? { clip }
            : {}),
        })
        console.log(`Generated ${shot.name}-${mode}.png${clip
          ? ` (${clip.width}x${clip.height})`
          : ` (${VIEWPORT.width}x${VIEWPORT.height})`}`)
      }
    }
    await context.close()
  }
  finally {
    await browser.close()
    server.close()
    highlighter.dispose()
  }
}

main().catch((err) => {
  console.error(err)
  process.exitCode = 1
})
