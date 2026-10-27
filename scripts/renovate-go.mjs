import { resolve } from 'node:path'
import process from 'node:process'
import { DEPENDENCY_INPUT_FILES } from './renovate-inputs.mjs'
import { refreshSite } from './renovate-site.mjs'

const GENPROTO = 'google.golang.org/genproto'

export function replacementVersion(moduleText) {
  const values = [...moduleText.matchAll(/^\s*(?:replace\s+)?google\.golang\.org\/genproto\s*=>\s*google\.golang\.org\/genproto\s+(\S+)/gm)].map(match => match[1])
  if (values.length !== 1)
    throw new Error('Each workspace module must contain exactly one genproto replacement.')
  return values[0]
}

export function verifyReplacements(texts) {
  const versions = texts.map(replacementVersion)
  if (new Set(versions).size !== 1)
    throw new Error('Conflicting genproto replacements; both modules must change together before workspace commands.')
}

export function newModulePath(upgrade) {
  const old = upgrade.depName
  const major = Number(/^v(\d+)\./.exec(upgrade.newValue)?.[1])
  if (!Number.isSafeInteger(major))
    throw new Error(`Invalid Go module version: ${upgrade.newValue}`)
  if (upgrade.newValue.endsWith('+incompatible'))
    return old
  if (old.startsWith('gopkg.in/'))
    return old.replace(/\.v\d+(?:-unstable)?$/, `.v${major}`)
  const base = old.replace(/\/v\d+$/, '')
  return major >= 2 ? `${base}/v${major}` : base
}

/** Prepare validated Go upgrades before any updated generator runs. */
export function prepareGoUpgrades(upgrades, { trusted, run, read }) {
  const hasWorkspaceGo = upgrades.some(upgrade => upgrade.manager === 'gomod' && upgrade.packageFile !== 'site/go.mod')
  const hasSiteGo = upgrades.some(upgrade => upgrade.manager === 'gomod' && upgrade.packageFile === 'site/go.mod')
  const state = { hasWorkspaceGo, hasSiteGo }
  if (!hasWorkspaceGo && !hasSiteGo)
    return state
  if (hasWorkspaceGo) {
    let texts = ['backend/go.mod', 'desktop/go/go.mod'].map(read)
    const targets = [...new Set(upgrades.filter(upgrade => upgrade.manager === 'gomod' && upgrade.packageFile !== 'site/go.mod' && upgrade.depName === GENPROTO && upgrade.depType === 'replace').map(upgrade => upgrade.newValue))]
    if (targets.length > 1)
      throw new Error('The genproto update specifies conflicting versions.')
    // Renovate can report only one replacement occurrence. Apply its single target to both
    // module files without entering workspace mode, then validate before any workspace command.
    if (targets.length === 1) {
      for (const cwd of ['backend', 'desktop/go'])
        run('go', ['mod', 'edit', `-replace=${GENPROTO}=${GENPROTO}@${targets[0]}`], { cwd, env: { GOWORK: 'off' } })
      texts = ['backend/go.mod', 'desktop/go/go.mod'].map(read)
    }
    verifyReplacements(texts)
  }
  const goUpgrades = upgrades.filter(upgrade => upgrade.manager === 'gomod' && upgrade.newValue && !['golang', 'toolchain'].includes(upgrade.depType))
  const changes = new Map()
  for (const upgrade of goUpgrades) {
    const target = newModulePath(upgrade)
    if (target === upgrade.depName)
      continue
    const key = JSON.stringify([upgrade.depName, target, upgrade.newValue])
    const change = changes.get(key) ?? { old: upgrade.depName, new: target, version: upgrade.newValue, directories: [] }
    change.directories = [...new Set([...change.directories, ...(upgrade.packageFile === 'site/go.mod' ? ['site'] : ['backend', 'desktop/go'])])]
    changes.set(key, change)
  }
  const unique = [...changes.values()]
  run(resolve(trusted, `.tmp/leapmux-dependency-imports${process.platform === 'win32' ? '.exe' : ''}`), [], { input: JSON.stringify(unique) })
  for (const upgrade of goUpgrades) {
    if (upgrade.depType === 'replace')
      continue
    run('go', ['mod', 'edit', `-require=${newModulePath(upgrade)}@${upgrade.newValue}`], { cwd: upgrade.packageFile.replace('/go.mod', ''), env: { GOWORK: 'off' } })
  }
  // Updated tools may import newly moved transitive modules. Resolve all metadata and
  // checksums before go tool sqlc or any other updated tool runs.
  for (const file of DEPENDENCY_INPUT_FILES.gomod.filter(file => file === 'site/go.mod' ? hasSiteGo : hasWorkspaceGo))
    run('go', ['mod', 'download', 'all'], { cwd: file.replace('/go.mod', ''), env: { GOWORK: 'off', GOFLAGS: '-mod=mod' } })
  if (hasWorkspaceGo) {
    run('go', ['work', 'sync'])
    verifyReplacements(['backend/go.mod', 'desktop/go/go.mod'].map(read))
  }
  return state
}

/** Generate and tidy only the graphs selected during preparation. */
export function finishGoUpgrades({ hasWorkspaceGo, hasSiteGo }, { root, run, read, write }) {
  if (hasWorkspaceGo) {
    run('task', ['prepare-backend'])
    for (const cwd of ['backend', 'desktop/go'])
      run('go', ['mod', 'tidy'], { cwd })
    run('go', ['work', 'sync'])
  }
  if (hasSiteGo)
    refreshSite({ root, run, read, write })
}
