#!/usr/bin/env bun
import { spawnSync } from 'node:child_process'
import { appendFileSync, readFileSync } from 'node:fs'
import process from 'node:process'
import { outputState } from './task-state.mjs'

export const NOTICE_INPUTS = [
  'go.work',
  'go.work.sum',
  'backend/go.mod',
  'backend/go.sum',
  'desktop/go/go.mod',
  'desktop/go/go.sum',
  // The runtime dependency closure includes test and platform-specific imports.
  'backend/**/*.go',
  'desktop/go/**/*.go',
  'backend/**/sqlc.yaml',
  'backend/**/*.sql',
  'proto/**/*.proto',
  'buf.gen.yaml',
  'contracts/**/*.json',
  'scripts/generate-contracts.mjs',
  'desktop/rust/Cargo.toml',
  'desktop/rust/Cargo.lock',
  'frontend/package.json',
  'frontend/bun.lock',
  'frontend/bunfig.toml',
  'frontend/.npmrc',
  'frontend/patches/**',
  'frontend/src/styles/themes/default.ts',
  'scripts/task-state.mjs',
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

export function noticeInputState(root = process.cwd(), outputs = []) {
  if (!Array.isArray(outputs) || outputs.some(pattern => typeof pattern !== 'string'))
    throw new Error('NOTICE output patterns must be an array of strings.')
  const exclusions = outputs.map(pattern => new Bun.Glob(pattern))
  const patterns = NOTICE_INPUTS.filter(pattern => [...new Bun.Glob(pattern).scanSync({ cwd: root, dot: true, onlyFiles: true })]
    .some(file => !exclusions.some(glob => glob.match(file.replaceAll('\\', '/')))))
  return outputState(root, [...patterns, ...outputs.map(exclude => ({ exclude }))])
}

export function selectNoticeInputs(event, { run = args => spawnSync('git', args, { encoding: 'utf8', timeout: 30000 }), warn = console.warn } = {}) {
  const base = event.pull_request?.base.sha ?? event.before
  const head = event.pull_request?.head.sha ?? event.after
  if (!/^[a-f0-9]{40}$/.test(base ?? '') || !/^[a-f0-9]{40}$/.test(head ?? '') || /^0+$/.test(base) || /^0+$/.test(head))
    return true
  const missing = [...new Set([base, head])].filter(sha => run(['cat-file', '-e', `${sha}^{commit}`]).status !== 0)
  if (missing.length && run(['fetch', '--depth=1', '--no-tags', 'origin', ...missing]).status !== 0) {
    warn('NOTICE comparison commits are unavailable; generation is required.')
    return true
  }
  const result = run(['diff', '--no-renames', '--name-only', '-z', base, head, '--'])
  if (result.status !== 0) {
    warn('NOTICE input comparison failed; generation is required.')
    return true
  }
  return needsNotice(result.stdout.split('\0').filter(Boolean))
}

if (import.meta.main) {
  if (process.argv.length === 4 && process.argv[2] === '--digest') {
    console.log(noticeInputState(process.cwd(), JSON.parse(process.argv[3])))
  }
  else {
    if (process.argv.length !== 2)
      throw new Error('Use --digest with a JSON list of output patterns, or pass no arguments for selection.')
    const event = JSON.parse(readFileSync(process.env.GITHUB_EVENT_PATH, 'utf8'))
    const needed = selectNoticeInputs(event)
    appendFileSync(process.env.GITHUB_OUTPUT, `needed=${needed}\n`)
  }
}
