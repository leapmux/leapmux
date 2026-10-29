import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'bun:test'
import { renovateModule } from './renovate-engine.mjs'
import { dependencyIgnorePaths, isDependencyInput, isGithubActionSource } from './renovate-inputs.mjs'

const { getConfig } = await renovateModule('config/defaults.js')
const { getManagerConfig } = await renovateModule('config/index.js')
const { mergeChildConfig } = await renovateModule('config/utils.js')
const { init: initializeRenovateLogger } = await renovateModule('logger/index.js')
const { getFilteredFileList, getMatchingFiles } = await renovateModule('workers/repository/extract/file-match.js')
await initializeRenovateLogger()

describe('isGithubActionSource', () => {
  it('accepts configured workflows and nested composite actions with either YAML extension', () => {
    for (const path of [
      '.github/workflows/ci.yaml',
      '.github/workflows/ci.yml',
      '.github/actions/build/action.yaml',
      '.github/actions/build/linux/action.yml',
    ])
      expect(isGithubActionSource(path), path).toBe(true)
  })
  it('rejects traversal, absolute paths, control characters, and actions outside the configured scope', () => {
    for (const path of [
      '.github/actions/../action.yaml',
      '.github/actions/build/../../action.yaml',
      '.github/actions//action.yaml',
      '/.github/actions/a/action.yaml',
      '.github/actions/a\\b/action.yaml',
      '.github/actions/a\n/action.yaml',
      'other/action.yml',
      null,
    ])
      expect(isGithubActionSource(path), String(path)).toBe(false)
  })
})

describe('dependencyIgnorePaths', () => {
  it('filters the real additive Actions defaults before any forced security configuration', async () => {
    const allowed = ['.github/workflows/ci.yaml', '.github/actions/build/linux/action.yaml']
    const foreign = [
      'action.yml',
      'scripts/sample/action.yaml',
      'example/.github/workflows/ci.yaml',
      'example/[special]/action.yml',
    ]
    const metadata = ['scripts/sample/action.yaml-tools/Cargo.toml', '.github/actions/build/example/.github/workflows/ci.yaml/action.yaml', 'example/[special]/action.yml-tools/package.json']
    const configuration = JSON.parse(readFileSync(new URL('../renovate.json', import.meta.url), 'utf8'))
    const ignorePaths = await dependencyIgnorePaths([
      ...allowed,
      ...foreign,
      ...metadata,
    ], configuration)
    expect(ignorePaths).toContain('**/node_modules/**')
    const effective = mergeChildConfig(mergeChildConfig(getConfig(), configuration), { ignorePaths, force: configuration.vulnerabilityAlerts })
    expect(getMatchingFiles(getManagerConfig(effective, 'github-actions'), [
      ...allowed,
      ...foreign,
    ])).toEqual(allowed.toSorted())
    expect(getFilteredFileList(effective, metadata)).toEqual(metadata)
  })
})

describe('isDependencyInput', () => {
  it('limits non-action managers to the approved metadata files', () => {
    expect(isDependencyInput('gomod', 'backend/go.mod')).toBe(true)
    expect(isDependencyInput('gomod', 'site/go.mod')).toBe(true)
    expect(isDependencyInput('bun', '.github/renovate/package.json')).toBe(true)
    expect(isDependencyInput('cargo', 'desktop/rust/Cargo.toml')).toBe(true)
    for (const [manager, path] of [
      ['unknown', 'versions.env'],
      ['gomod', '../go.mod'],
      ['bun', 'other/package.json'],
    ])
      expect(isDependencyInput(manager, path)).toBe(false)
  })
})
