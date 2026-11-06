import { resolve } from 'node:path'
import process from 'node:process'
import { newModulePath } from './go-module-path.mjs'
import { GO_TOOL_MODULES, GO_WORKSPACE_MODULES } from './go-tools.mjs'
import { refreshSite } from './renovate-site.mjs'

/** Prepare validated Go upgrades before any updated generator runs. */
export function prepareGoUpgrades(upgrades, { trusted, run }) {
  const hasWorkspaceGo = upgrades.some(upgrade => upgrade.manager === 'gomod' && GO_WORKSPACE_MODULES.includes(upgrade.packageFile))
  const hasSiteGo = upgrades.some(upgrade => upgrade.manager === 'gomod' && upgrade.packageFile === 'site/go.mod')
  const toolModules = GO_TOOL_MODULES.filter(file => upgrades.some(upgrade => upgrade.manager === 'gomod' && upgrade.packageFile === file))
  const state = { hasWorkspaceGo, hasSiteGo, toolModules }
  if (!hasWorkspaceGo && !hasSiteGo && toolModules.length === 0)
    return state
  const goUpgrades = upgrades.filter(upgrade => upgrade.manager === 'gomod' && upgrade.newValue && !['golang', 'toolchain'].includes(upgrade.depType))
  const changes = new Map()
  for (const upgrade of goUpgrades) {
    const target = newModulePath(upgrade)
    if (target === upgrade.depName)
      continue
    const key = JSON.stringify([upgrade.depName, target, upgrade.newValue])
    const change = changes.get(key) ?? {
      old: upgrade.depName,
      new: target,
      version: upgrade.newValue,
      directories: [],
    }
    change.directories = [
      ...new Set([
        ...change.directories,
        ...(GO_WORKSPACE_MODULES.includes(upgrade.packageFile)
          ? GO_WORKSPACE_MODULES.map(file => file.replace('/go.mod', ''))
          : [
              upgrade.packageFile.replace('/go.mod', ''),
            ]),
      ]),
    ]
    changes.set(key, change)
  }
  const unique = [
    ...changes.values(),
  ]
  // Rewrite moved module paths in imports and tool directives before any metadata changes.
  run(resolve(trusted, `.tmp/leapmux-dependency-imports${process.platform === 'win32'
    ? '.exe'
    : ''}`), [], {
    input: JSON.stringify(unique),
  })
  for (const upgrade of goUpgrades) {
    if (upgrade.depType === 'replace')
      continue
    run('go', ['mod', 'edit', `-require=${newModulePath(upgrade)}@${upgrade.newValue}`], {
      cwd: upgrade.packageFile.replace('/go.mod', ''),
      env: { GOWORK: 'off' },
    })
  }
  // Resolve metadata before an updated generator runs. Keep unrelated graphs untouched.
  const modules = [
    ...(hasWorkspaceGo
      ? GO_WORKSPACE_MODULES
      : []),
    ...(hasSiteGo
      ? ['site/go.mod']
      : []),
    ...toolModules,
  ]
  for (const file of modules) {
    run('go', ['mod', 'download', 'all'], {
      cwd: file.replace('/go.mod', ''),
      env: { GOWORK: 'off', GOFLAGS: '-mod=mod' },
    })
  }
  if (hasWorkspaceGo) {
    // go work sync refreshes go.work.sum. .gitignore and the post-upgrade file filters
    // keep that file out of every commit.
    run('go', ['work', 'sync'])
  }
  return state
}

/** Tidy updated tools and refresh the applications after their code changes. */
export function finishGoUpgrades({ hasWorkspaceGo, hasSiteGo, toolModules }, { root, run, read, write }) {
  for (const file of toolModules) {
    run('go', ['mod', 'tidy'], {
      cwd: file.replace('/go.mod', ''),
      env: { GOWORK: 'off' },
    })
  }
  if (hasWorkspaceGo || toolModules.includes('tools/sqlc/go.mod')) {
    run('task', ['prepare-backend'])
    // Generated code can introduce runtime imports. Resolve them after generation,
    // without adding the generator's own dependencies to the application graph.
    for (const file of GO_WORKSPACE_MODULES) {
      run('go', ['mod', 'tidy'], {
        cwd: file.replace('/go.mod', ''),
      })
    }
    run('go', ['work', 'sync'])
  }
  if (hasSiteGo)
    refreshSite({ root, run, read, write })
}
