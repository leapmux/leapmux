import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { renovateModule } from './renovate-engine.mjs'

const { getConfig } = await renovateModule('config/defaults.js')
const { getManagerConfig } = await renovateModule('config/index.js')
const { resolveConfigPresets } = await renovateModule('config/presets/index.js')
const { mergeChildConfig } = await renovateModule('config/utils.js')
const { matchRegexOrGlob } = await renovateModule('util/string-match.js')
const { getMatchingFiles } = await renovateModule('workers/repository/extract/file-match.js')
const policy = JSON.parse(readFileSync(resolve(import.meta.dirname, '../renovate.json'), 'utf8'))
export const DEPENDENCY_INPUT_FILES = Object.freeze({
  gomod: Object.freeze(['backend/go.mod', 'desktop/go/go.mod', 'site/go.mod']),
  bun: Object.freeze(['frontend/package.json', '.github/renovate/package.json']),
  cargo: Object.freeze(['desktop/rust/Cargo.toml']),
  // Renovate uses "regex" in post-update metadata for the configured custom.regex manager.
  regex: Object.freeze(['versions.env']),
})

export function isGithubActionSource(path) {
  if (typeof path !== 'string' || /[\0\r\n\\]/.test(path) || path.split('/').some(part => part === '' || part === '.' || part === '..'))
    return false
  return policy['github-actions'].managerFilePatterns.some(pattern => matchRegexOrGlob(path, pattern))
}

export function isDependencyInput(manager, path) {
  return (DEPENDENCY_INPUT_FILES[manager] ?? []).includes(path) || (manager === 'github-actions' && isGithubActionSource(path))
}

export async function dependencyConfiguration(configuration = policy) {
  const resolved = await resolveConfigPresets(structuredClone(configuration))
  return mergeChildConfig(getConfig(), resolved.config)
}

export async function dependencyIgnorePaths(files, configuration = policy) {
  const effective = await dependencyConfiguration(configuration)
  const candidates = getMatchingFiles(getManagerConfig(effective, 'github-actions'), files)
  const exclusions = candidates.filter(file => !isGithubActionSource(file)).map((file) => {
    // ignorePaths also matches substrings. An escaped glob preserves sibling metadata files.
    const literal = file.replace(/([\\*?[\]{}()!+@])/g, '\\$1')
    return literal.startsWith('\\')
      ? literal
      : `\\${literal}`
  })
  return [
    ...new Set([
      ...(effective.ignorePaths ?? []),
      ...exclusions,
    ]),
  ]
}
