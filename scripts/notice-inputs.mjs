#!/usr/bin/env bun
import { spawnSync } from 'node:child_process'
import { appendFileSync, readFileSync } from 'node:fs'
import process from 'node:process'

export const NOTICE_INPUTS = [
  'go.work',
  'go.work.sum',
  '**/go.mod',
  '**/go.sum',
  'desktop/rust/Cargo.toml',
  'desktop/rust/Cargo.lock',
  'frontend/package.json',
  'frontend/bun.lock',
  'scripts/generate-notice.mjs',
  'scripts/notice-inputs.mjs',
  'scripts/license-overrides/**',
  'Taskfile.yaml',
  'NOTICE.md',
  'NOTICE.html',
  '.github/workflows/notice.yaml',
  '.github/actions/setup-build/action.yaml',
  '.github/actions/build-output-cache/action.yaml',
  '.github/actions/dependency-caches/action.yaml',
  '.github/actions/load-versions/action.yaml',
]

export function needsNotice(files) {
  return files.some(file => NOTICE_INPUTS.some(pattern => new Bun.Glob(pattern).match(file)))
}

if (import.meta.main) {
  const event = JSON.parse(readFileSync(process.env.GITHUB_EVENT_PATH, 'utf8'))
  const base = event.pull_request?.base.sha ?? event.before
  const head = event.pull_request?.head.sha ?? event.after
  // A missing or unreachable base requires generation; it must never skip the check.
  let needed = true
  if (/^[a-f0-9]{40}$/.test(base ?? '') && /^[a-f0-9]{40}$/.test(head ?? '') && !/^0+$/.test(base)) {
    const result = spawnSync('git', ['diff', '--name-only', '-z', base, head, '--'], { encoding: 'utf8' })
    if (result.status === 0)
      needed = needsNotice(result.stdout.split('\0').filter(Boolean))
  }
  appendFileSync(process.env.GITHUB_OUTPUT, `needed=${needed}\n`)
}
