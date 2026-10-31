// The release age governs the dependency versions of these registry ecosystems. This one
// table gives each ecosystem's name in GitHub advisories and Dependabot alerts, and the
// kind of its dependency files with their names. The kind is also the name of the Renovate
// manager that updates those files. Every other table of these names derives from this
// one. The module imports nothing, so every module can import it, including the dashboard,
// which Renovate loads under Node.

export const ECOSYSTEMS = Object.freeze({
  npm: Object.freeze({
    advisory: 'npm',
    kind: 'bun',
    manifest: 'package.json',
    lockfile: 'bun.lock',
  }),
  crates: Object.freeze({
    advisory: 'rust',
    kind: 'cargo',
    manifest: 'Cargo.toml',
    lockfile: 'Cargo.lock',
  }),
  go: Object.freeze({
    advisory: 'go',
    kind: 'gomod',
    manifest: 'go.mod',
    lockfile: 'go.mod',
  }),
})

/** Return the GitHub advisory ecosystem of an ecosystem of this table, and fail for any other name. */
export function advisoryEcosystem(ecosystem) {
  if (!Object.hasOwn(ECOSYSTEMS, ecosystem))
    throw new Error(`Unknown advisory ecosystem: ${ecosystem}`)
  return ECOSYSTEMS[ecosystem].advisory
}

/** Return the ecosystem of this table that a GitHub advisory ecosystem names, or null for an unmanaged one. */
export function ecosystemOfAdvisory(advisory) {
  return Object.keys(ECOSYSTEMS).find(ecosystem => ECOSYSTEMS[ecosystem].advisory === advisory) ?? null
}
