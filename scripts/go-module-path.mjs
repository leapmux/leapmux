/**
 * Return the module path of a Go upgrade's new version. A major version of 2 or more lives
 * under a `/vN` path, or a `.vN` path on gopkg.in, unless the version is `+incompatible`.
 */
export function newModulePath(upgrade) {
  const old = upgrade.depName
  const major = Number(/^v(\d+)\./.exec(upgrade.newValue)?.[1])
  if (!Number.isSafeInteger(major))
    throw new TypeError(`Invalid Go module version: ${upgrade.newValue}`)
  if (upgrade.newValue.endsWith('+incompatible'))
    return old
  if (old.startsWith('gopkg.in/'))
    return old.replace(/\.v\d+(?:-unstable)?$/, `.v${major}`)
  const base = old.replace(/\/v\d+$/, '')
  return major >= 2
    ? `${base}/v${major}`
    : base
}
