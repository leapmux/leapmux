// Filesystem implementation of the cache snapshot lifecycle. Every path
// comes from the policy registry in scripts/cache-policy.mjs: no helper here
// accepts a deletion root or a payload location from workflow text.
//
// The lifecycle of one cache kind:
// 1. The pinned restore transport fills the staging payload directory.
// 2. `activateCacheSnapshot` validates the payload and activates it, or
//    starts a writer from empty owned roots.
// 3. The workload produces into the active roots.
// 4. `prepareCacheSnapshot` runs the trimming rule, validates required
//    contents, builds the payload, and checks capacity before the transport
//    saves the archive.
// 5. `cleanupSnapshotStaging` removes the staging root in an always() step.
import { spawnSync } from 'node:child_process'
import {
  appendFileSync,
  chmodSync,
  copyFileSync,
  lstatSync,
  mkdirSync,
  readdirSync,
  readlinkSync,
  renameSync,
  rmSync,
  statfsSync,
  symlinkSync,
  unlinkSync,
  utimesSync,
} from 'node:fs'
import path, { basename, dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import {
  activeRootsFor,
  cacheKey,
  cachePolicy,
  partitionPrefix,
  restoreKeys,
  rootActivePath,
  rootPayloadPath,
  snapshotPaths,
  validateCacheKey,
} from './cache-policy.mjs'

// A capacity skip needs room for one payload copy, one compressed archive
// temporary, and a fixed headroom. An inode skip needs room for two link
// targets per selected file.
const CAPACITY_BYTES_MULTIPLIER = 2
const CAPACITY_BYTES_HEADROOM = 1024 * 1024 * 1024
const CAPACITY_INODE_MULTIPLIER = 2

const UNAVAILABLE = 'unavailable'

/** Measure bytes and files of a tree without following symbolic links. */
export function measureCacheTree(directory) {
  const stat = lstatSync(directory)
  if (!stat.isDirectory())
    return { bytes: stat.size, files: 1, directories: 0 }
  let bytes = 0
  let files = 0
  let directories = 1
  const stack = [directory]
  while (stack.length > 0) {
    const current = stack.pop()
    for (const entry of readdirSync(current, { withFileTypes: true })) {
      const path = join(current, entry.name)
      if (entry.isSymbolicLink()) {
        // A symlink contributes its own link bytes, never its target.
        bytes += lstatSync(path).size
        files++
        continue
      }
      if (entry.isDirectory()) {
        directories++
        stack.push(path)
        continue
      }
      bytes += lstatSync(path).size
      files++
    }
  }
  return { bytes, files, directories }
}

/**
 * Free disk bytes and available inodes of the volume that holds `directory`,
 * or null fields when the filesystem does not expose them.
 */
export function filesystemCapacity(directory, statfs = statfsSync) {
  try {
    const stat = statfs(directory)
    return {
      freeBytes: Number(stat.bavail) * Number(stat.bsize),
      availableInodes: Number(stat.files) > 0
        ? Number(stat.ffree)
        : null,
    }
  }
  catch {
    return { freeBytes: null, availableInodes: null }
  }
}

function formatCount(value) {
  return value === null
    ? UNAVAILABLE
    : String(value)
}

/**
 * Copy one file, symlink, or directory tree. Symbolic links are recreated
 * verbatim and never traversed. Modes and modification times survive the
 * copy, which keeps timestamp-sensitive build inputs reusable across a
 * snapshot round trip.
 */
function copyEntryPreserving(source, destination) {
  const stat = lstatSync(source)
  if (stat.isSymbolicLink()) {
    removePath(destination)
    symlinkSync(readlinkSync(source), destination)
    return
  }
  if (stat.isFile()) {
    if (pathExists(destination) && !lstatSync(destination).isFile())
      removePath(destination)
    try {
      copyFileSync(source, destination)
    }
    catch (error) {
      // A merged snapshot can overwrite a file that a tool cache marked
      // read-only, such as a Git object from a previous restore.
      if (error.code !== 'EACCES' && error.code !== 'EPERM')
        throw error
      chmodSync(destination, stat.mode | 0o200)
      copyFileSync(source, destination)
    }
    chmodSync(destination, stat.mode)
    utimesSync(destination, stat.atime, stat.mtime)
    return
  }
  if (stat.isDirectory()) {
    if (pathExists(destination) && !lstatSync(destination).isDirectory())
      removePath(destination)
    mkdirSync(destination, { recursive: true })
    chmodSync(destination, stat.mode | 0o200)
    for (const entry of readdirSync(source, { withFileTypes: true }))
      copyEntryPreserving(join(source, entry.name), join(destination, entry.name))
    // The directory mode applies only after its children exist: a tool
    // cache such as Go's marks directories read-only, and writing into a
    // copy that already carries that mode would fail.
    chmodSync(destination, stat.mode)
    utimesSync(destination, stat.atime, stat.mtime)
    return
  }
  throw new Error(`Cannot copy a cache entry that is neither file, directory, nor symlink: ${source}`)
}

function pathExists(path) {
  // lstat: a dangling symlink counts as present content of the cache.
  try {
    lstatSync(path)
    return true
  }
  catch (error) {
    if (error.code === 'ENOENT' || error.code === 'ENOTDIR')
      return false
    throw error
  }
}

function makeTreeWritable(path) {
  // Tool caches mark directories read-only, and removing an entry requires
  // a writable parent. Restore the owner's write bit on every directory
  // before a retry; file modes need no change.
  const stack = [path]
  while (stack.length > 0) {
    const current = stack.pop()
    let stat
    try {
      stat = lstatSync(current)
    }
    catch {
      continue
    }
    if (stat.isSymbolicLink())
      continue
    if (!stat.isDirectory())
      continue
    try {
      chmodSync(current, stat.mode | 0o200)
    }
    catch {}
    for (const entry of readdirSync(current, { withFileTypes: true }))
      stack.push(join(current, entry.name))
  }
}

function removePath(path) {
  if (!pathExists(path))
    return
  // rmSync removes a symlink itself and never descends into its target.
  try {
    rmSync(path, { recursive: true, force: true })
  }
  catch {
    makeTreeWritable(path)
    rmSync(path, { recursive: true, force: true })
  }
}

export function isWithinDirectory(parent, candidate, paths = path) {
  const normalized = paths.resolve(candidate)
  const expected = paths.resolve(parent)
  const relative = paths.relative(expected, normalized)
  return relative === '' || (!paths.isAbsolute(relative) && relative !== '..' && !relative.startsWith(`..${paths.sep}`))
}

function assertInsideStaging(candidate, kind, workspace) {
  const normalized = resolve(candidate)
  const expected = resolve(snapshotPaths(kind, workspace).stagingRoot)
  if (!isWithinDirectory(expected, normalized))
    throw new Error(`Refusing to operate on ${normalized}: it is not inside the ${kind} staging root.`)
}

/** Move a tree with a same-filesystem rename, or a checked copy across volumes. */
function moveTree(source, destination, kind, log, rename = renameSync, capacityImpl = filesystemCapacity) {
  try {
    rename(source, destination)
    return
  }
  catch (error) {
    if (error.code !== 'EXDEV')
      throw error
  }
  const size = measureCacheTree(source)
  requireCopyCapacity(source, destination, kind, capacityImpl)
  log(`Copying ${size.bytes} bytes across volumes from ${source} to ${destination}.`)
  copyEntryPreserving(source, destination)
  removePath(source)
}

function validatePayloadRoots(kind, roots) {
  const problems = []
  for (const root of roots) {
    if (!pathExists(root.payload)) {
      if (root.required)
        problems.push(`required root ${root.name} is absent from the restored payload`)
      continue
    }
    const stat = lstatSync(root.payload)
    if (root.type === 'file' && !stat.isFile())
      problems.push(`root ${root.name} must be a regular file in the payload`)
    if (root.type === 'tree' && !stat.isDirectory())
      problems.push(`root ${root.name} must be a directory in the payload`)
    if (root.requiredNonEmpty && stat.isDirectory() && readdirSync(root.payload).length === 0)
      problems.push(`required root ${root.name} must hold at least one entry`)
  }
  if (problems.length > 0)
    return { valid: false, reason: `The ${kind} payload is unusable: ${problems.join('; ')}.` }
  return { valid: true }
}

function activateRoot(root, kind, log, fsImpl = {}) {
  const rename = fsImpl.rename ?? renameSync
  const capacityImpl = fsImpl.capacity ?? filesystemCapacity
  const copy = fsImpl.copy ?? copyEntryPreserving
  const keepPayload = cachePolicy(kind).population === 'isolated-download'
  if (root.type === 'file') {
    mkdirSync(dirname(root.active), { recursive: true })
    requireCopyCapacity(root.payload, dirname(root.active), kind, capacityImpl)
    copy(root.payload, root.active)
    if (!keepPayload)
      unlinkSync(root.payload)
    return
  }
  if (root.mode === 'merge') {
    // Merge into the active downloads without removing anything, so the next
    // consumer reads the dependency without downloading it again. The
    // payload stays in place for the archive transport.
    requireCopyCapacity(root.payload, root.active, kind, capacityImpl)
    copy(root.payload, root.active)
    return
  }
  // Replace the active root with the payload root. The payload of a compiled
  // kind is dead after activation, so a same-filesystem rename moves it.
  removePath(root.active)
  mkdirSync(dirname(root.active), { recursive: true })
  if (keepPayload) {
    copy(root.payload, root.active)
    return
  }
  moveTree(root.payload, root.active, kind, log, rename, capacityImpl)
}

function clearOwnedRoots(roots, log) {
  for (const root of roots) {
    if (root.type === 'file') {
      if (root.clearOnWriterMiss)
        removePath(root.active)
      else
        mkdirSync(dirname(root.active), { recursive: true })
      continue
    }
    if (!root.clearOnWriterMiss) {
      // Dependency roots keep whatever active downloads they already hold.
      mkdirSync(root.active, { recursive: true })
      continue
    }
    log(`Removing the active root ${root.active} for an empty writer start.`)
    removePath(root.active)
    mkdirSync(root.active, { recursive: true })
  }
}

/**
 * A restored archive is a usable fallback only when its key matches the
 * current key shape of the kind. A key that predates a shape change (the
 * weekly tauri-tools keys that the content-addressed keys replaced) arrives
 * only through prefix matching, and is not a snapshot of this kind at all.
 */
function usableFallbackKey(kind, matchedKey, env, log) {
  if (!matchedKey)
    return null
  try {
    validateCacheKey(kind, matchedKey, env)
    return matchedKey
  }
  catch {
    log(`Ignoring the restored ${kind} archive ${matchedKey}: its key does not match the current ${kind} key shape.`)
    return null
  }
}

/**
 * Validate a restored payload and activate it, or start a writer from empty
 * owned roots. A reader may activate a fallback payload, but the caller
 * never saves after a fallback. A writer must not receive a fallback at all.
 */
export async function activateCacheSnapshot({
  kind,
  key,
  writer = false,
  exactHit = false,
  matchedKey = null,
  workspace = process.cwd(),
  env = process.env,
  log = () => {},
  fsImpl = {},
}) {
  const policy = cachePolicy(kind)
  validateCacheKey(kind, key, env)
  const fallbackKey = usableFallbackKey(kind, matchedKey, env, log)
  if (writer && fallbackKey && !exactHit)
    throw new Error(`A ${kind} writer must not restore a fallback snapshot. Writers start from empty owned roots.`)
  const roots = activeRootsFor(kind, env, workspace)
  const { payloadRoot } = snapshotPaths(kind, workspace)
  let validation = { valid: false, reason: 'no payload was restored' }
  if (pathExists(payloadRoot))
    validation = validatePayloadRoots(kind, roots)
  const usableExact = exactHit && validation.valid
  if (exactHit && !validation.valid)
    log(`An exact hit restored an unusable payload: ${validation.reason} Rebuilding active data without replacing the occupied archive key.`)
  if (usableExact || (!exactHit && fallbackKey && validation.valid)) {
    log(`${usableExact ? 'Activating an exact' : 'Activating a fallback'} ${kind} snapshot.`)
    for (const root of roots) {
      // An optional root that the payload does not carry stays untouched.
      if (!pathExists(root.payload)) {
        if (root.required)
          throw new Error(`The ${kind} required root ${root.name} is absent from the payload.`)
        continue
      }
      activateRoot(root, kind, log, fsImpl)
    }
    return {
      outcome: usableExact ? 'activated-exact' : 'activated-fallback',
      cacheHit: usableExact,
      saveRequired: false,
      activePath: roots[0].active,
    }
  }
  removePath(payloadRoot)
  if (writer) {
    // A writer owns an empty payload root from this point on: the population
    // of an isolated-download kind writes into it directly.
    mkdirSync(payloadRoot, { recursive: true })
    if (policy.population === 'isolated-download') {
      for (const root of roots) mkdirSync(root.payload, { recursive: true })
    }
    clearOwnedRoots(roots, log)
    return {
      outcome: 'writer-empty',
      cacheHit: false,
      saveRequired: !exactHit,
      activePath: roots[0].active,
    }
  }
  // A reader miss still leaves a usable empty active root, so a consumer
  // such as the Docker mount injection finds a directory it can read.
  for (const root of roots) {
    if (root.type === 'tree')
      mkdirSync(root.active, { recursive: true })
  }
  return {
    outcome: 'miss',
    cacheHit: false,
    saveRequired: false,
    activePath: roots[0].active,
  }
}

/**
 * Copy the payload roots of a dependency kind into the active download
 * directories without removing active downloads, so the current job keeps
 * reading them after the staging payload is gone.
 */
export function publishDependencySnapshot({
  kind,
  key,
  workspace = process.cwd(),
  env = process.env,
  log = () => {},
  capacityImpl = filesystemCapacity,
}) {
  const policy = cachePolicy(kind)
  if (policy.population !== 'isolated-download')
    throw new Error(`publishDependencySnapshot applies to isolated-download kinds, not ${kind}.`)
  validateCacheKey(kind, key, env)
  const roots = activeRootsFor(kind, env, workspace)
  for (const root of roots) {
    if (!pathExists(root.payload)) {
      if (root.required)
        throw new Error(`The ${kind} payload root ${root.name} is absent; the download did not complete.`)
      continue
    }
    requireCopyCapacity(root.payload, root.active, kind, capacityImpl)
  }
  for (const root of roots) {
    if (!pathExists(root.payload))
      continue
    log(`Publishing the ${kind} payload root ${root.name} to ${root.active}.`)
    copyEntryPreserving(root.payload, root.active)
  }
}

function requireCopyCapacity(source, destination, kind, capacityImpl) {
  let existing = destination
  while (!pathExists(existing)) {
    const parent = dirname(existing)
    if (parent === existing)
      break
    existing = parent
  }
  const selected = measureCacheTree(source)
  const capacity = capacityImpl(existing)
  if ((capacity.freeBytes !== null && capacity.freeBytes < selected.bytes + CAPACITY_BYTES_HEADROOM)
    || (capacity.availableInodes !== null && capacity.availableInodes < selected.files + selected.directories)) {
    throw new Error(`Cannot copy the ${kind} cache: insufficient destination capacity (required bytes: ${selected.bytes + CAPACITY_BYTES_HEADROOM}, required filesystem entries: ${selected.files + selected.directories}).`)
  }
}

// --- Rust trimming, mirroring the pinned Swatinem/rust-cache cleanup -------

const CARGO_SAVE_TARGETS = new Set([
  'lib',
  'cdylib',
  'dylib',
  'rlib',
  'staticlib',
  'proc-macro',
])

function cargoMetadata(rustWorkspace, env) {
  const result = spawnSync('cargo', [
    'metadata',
    '--all-features',
    '--format-version',
    '1',
  ], {
    cwd: rustWorkspace,
    encoding: 'utf8',
    maxBuffer: 256 * 1024 * 1024,
    // The runner environment rides on the process environment, which keeps
    // PATH and HOME available to the toolchain.
    env: {
      ...process.env,
      ...env,
      CARGO_ENCODED_RUSTFLAGS: '',
    },
  })
  if (result.error || result.status !== 0)
    throw new Error(`cargo metadata failed in ${rustWorkspace}: ${result.stderr?.trim().slice(0, 500) ?? result.error?.message}`)
  return JSON.parse(result.stdout)
}

function rmExcept(directory, keep, log) {
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    let name = entry.name
    if (keep.has(name))
      continue
    // Cargo's v1 layout suffixes each package with its metadata hash; the v2
    // layout nests the hash and leaves bare package names. Check both.
    const idx = name.lastIndexOf('-')
    if (idx !== -1)
      name = name.slice(0, idx)
    if (!keep.has(name)) {
      log(`Trimming ${join(directory, entry.name)}.`)
      removePath(join(directory, entry.name))
    }
  }
}

function isProfileDirectory(path, name) {
  if (name === 'tests')
    return true
  return ['build', '.fingerprint', 'deps'].some(entry => pathExists(join(path, entry)))
}

function trimProfileTarget(profileDir, dependencies, log) {
  // Testing utility crates nest their own workspaces under target/tests.
  if (basename(profileDir) === 'tests') {
    for (const nested of ['target', 'trybuild']) {
      const path = join(profileDir, nested)
      if (pathExists(path))
        trimTargetDir(path, dependencies, log)
    }
    rmExcept(profileDir, new Set(['target', 'trybuild']), log)
    return
  }
  rmExcept(profileDir, new Set(['build', '.fingerprint', 'deps']), log)
  const keepPackage = new Set(dependencies.flatMap(entry => [
    entry.name,
    ...entry.targets.map(target => target.replaceAll('-', '_')),
  ]))
  const keepDeps = new Set(dependencies.flatMap((entry) => {
    const names = []
    for (const name of [entry.name, ...entry.targets]) {
      const underscored = name.replaceAll('-', '_')
      names.push(underscored, `lib${underscored}`)
    }
    return names
  }))
  for (const [subdir, keep] of [
    ['build', keepPackage],
    ['.fingerprint', keepPackage],
    ['deps', keepDeps],
  ]) {
    const path = join(profileDir, subdir)
    if (pathExists(path))
      rmExcept(path, keep, log)
  }
}

function trimTargetDir(targetDir, dependencies, log) {
  for (const entry of readdirSync(targetDir, { withFileTypes: true })) {
    const path = join(targetDir, entry.name)
    if (entry.isDirectory()) {
      if (isProfileDirectory(path, entry.name))
        trimProfileTarget(path, dependencies, log)
      else
        trimTargetDir(path, dependencies, log)
      continue
    }
    // Final binaries, packaging leftovers, and rustc metadata are files at
    // the target root. Keep only Cargo's own directory marker.
    if (entry.name !== 'CACHEDIR.TAG') {
      log(`Trimming ${path}.`)
      removePath(path)
    }
  }
}

/**
 * The native-rust trimming rule: remove application bundles, final
 * application binaries, incremental data, and packaging leftovers from the
 * active target directory, then identify the extracted -sys sources and the
 * Git checkouts that reusing the compiled artifacts requires. Modes and
 * source timestamps survive, because regenerated timestamps would trigger
 * dependency rebuilds.
 */
export function trimRustSnapshot({
  kind = 'native-rust',
  workspace = process.cwd(),
  env = process.env,
  payloadRoot,
  log = () => {},
}) {
  const roots = activeRootsFor(kind, env, workspace)
  const targetRoot = roots.find(root => root.name === 'target')
  if (!targetRoot || !pathExists(targetRoot.active))
    throw new Error(`The ${kind} trim requires the active target directory of desktop/rust.`)
  const before = measureCacheTree(targetRoot.active)
  const selections = []
  const rustWorkspace = join(workspace, 'desktop', 'rust')
  const metadata = cargoMetadata(rustWorkspace, env)
  const rustWorkspaceAbs = resolve(rustWorkspace)
  const dependencies = metadata.packages
    .filter(pkg => !isWithinDirectory(rustWorkspaceAbs, dirname(pkg.manifest_path)))
    .map(pkg => ({
      name: pkg.name,
      version: pkg.version,
      path: dirname(pkg.manifest_path),
      targets: pkg.targets
        .filter(entry => entry.kind.some(targetKind => CARGO_SAVE_TARGETS.has(targetKind)))
        .map(entry => entry.name),
    }))
  trimTargetDir(targetRoot.active, dependencies, log)

  const registrySrcRoot = roots.find(root => root.name === 'registry-src')
  const cargoHome = registrySrcRoot
    ? dirname(dirname(registrySrcRoot.active))
    : null
  if (cargoHome !== null) {
    // Extracted sources: cargo recreates them from the .crate cache, but
    // -sys crates check timestamps in their source tree, so a re-extraction
    // would rebuild them.
    const sysEntries = new Set(dependencies.filter(entry => entry.name.endsWith('-sys')).map(entry => `${entry.name}-${entry.version}`))
    const srcDir = join(cargoHome, 'registry', 'src')
    if (pathExists(srcDir)) {
      for (const registry of readdirSync(srcDir, { withFileTypes: true })) {
        if (!registry.isDirectory())
          continue
        for (const entry of readdirSync(join(srcDir, registry.name), { withFileTypes: true })) {
          if (!sysEntries.has(entry.name))
            continue
          selections.push({
            source: join(srcDir, registry.name, entry.name),
            destination: join(payloadRoot, 'registry-src', registry.name, entry.name),
          })
        }
      }
    }
    // Git checkouts: removing either the clone database or the checkout
    // triggers a rebuild, so the snapshot carries the checkouts of every
    // repository that supplies a dependency.
    const checkoutDir = join(cargoHome, 'git', 'checkouts')
    const checkoutRefs = new Map()
    for (const dependency of dependencies) {
      if (!isWithinDirectory(checkoutDir, dependency.path))
        continue
      const [repo, ref] = dependency.path.slice(checkoutDir.length + 1).split(/[/\\]/)
      if (!repo || !ref)
        continue
      if (!checkoutRefs.has(repo))
        checkoutRefs.set(repo, new Set())
      checkoutRefs.get(repo).add(ref)
    }
    if (pathExists(checkoutDir)) {
      for (const [repo, refs] of checkoutRefs) {
        for (const ref of refs) {
          const source = join(checkoutDir, repo, ref)
          if (!pathExists(source))
            continue
          selections.push({
            source,
            destination: join(payloadRoot, 'git-checkouts', repo, ref),
          })
        }
      }
    }
  }
  const after = measureCacheTree(targetRoot.active)
  return {
    selections,
    bytesBeforeTrim: before.bytes,
    filesBeforeTrim: before.files,
    bytesAfterTrim: after.bytes,
    filesAfterTrim: after.files,
  }
}

function dpkgFormat(fields) {
  return `${fields.map(field => `\${${field}}`).join('\t')}\n`
}

/** Select APT archives matching installed package versions and architectures. */
export function trimAptArchives({
  kind = 'apt-packages',
  workspace = process.cwd(),
  env = process.env,
  log = () => {},
  run = spawnSync,
}) {
  const root = activeRootsFor(kind, env, workspace).find(entry => entry.name === 'apt-archives')
  const { payloadRoot } = snapshotPaths(kind, workspace)
  const payloadDir = join(payloadRoot, root.name)
  const selections = []
  const installed = run('dpkg-query', ['--show', `--showformat=${dpkgFormat([
    'Package',
    'Version',
    'Architecture',
    'Status',
  ])}`], {
    encoding: 'utf8',
    maxBuffer: 16 * 1024 * 1024,
    env: {
      ...process.env,
      ...env,
    },
  })
  if (installed.error || installed.status !== 0)
    throw new Error(`Cannot read installed APT packages: ${installed.error?.message ?? installed.stderr?.trim()}`)
  const current = new Set(installed.stdout.split('\n').filter(Boolean).flatMap((line) => {
    const [name, version, arch, status] = line.split('\t')
    return status === 'install ok installed'
      ? [`${name}\t${version}\t${arch}`]
      : []
  }))
  let bytes = 0
  let files = 0
  let bytesBeforeTrim = 0
  let filesBeforeTrim = 0
  for (const entry of readdirSync(root.active, { withFileTypes: true })) {
    if (!entry.isFile() || !entry.name.endsWith('.deb'))
      continue
    const source = join(root.active, entry.name)
    bytesBeforeTrim += lstatSync(source).size
    filesBeforeTrim++
    const metadata = run('dpkg-deb', ['--show', `--showformat=${dpkgFormat(['Package', 'Version', 'Architecture'])}`, source], {
      encoding: 'utf8',
      env: {
        ...process.env,
        ...env,
      },
    })
    if (metadata.error || metadata.status !== 0)
      throw new Error(`Cannot read APT archive ${entry.name}: ${metadata.error?.message ?? metadata.stderr?.trim()}`)
    if (!current.has(metadata.stdout.trim()))
      continue
    selections.push({
      source,
      destination: join(payloadDir, entry.name),
    })
    bytes += lstatSync(source).size
    files++
  }
  log(`Selected ${files} APT archives matching installed package versions and architectures.`)
  return {
    selected: files,
    selections,
    bytesBeforeTrim,
    filesBeforeTrim,
    bytesAfterTrim: bytes,
    filesAfterTrim: files,
  }
}

function assertMainPush(env, kind) {
  if (env.GITHUB_REF !== 'refs/heads/main' || !['push', 'workflow_dispatch'].includes(env.GITHUB_EVENT_NAME)) {
    throw new Error(`The ${kind} cache publishes only from a main-branch push or dispatch; ref=${env.GITHUB_REF ?? 'unset'} event=${env.GITHUB_EVENT_NAME ?? 'unset'}.`)
  }
}

/**
 * Run the trimming rule of a kind, validate its required payload contents,
 * and build the staging payload without retaining a previous payload. A
 * malformed payload or a failed trim throws; insufficient capacity reports
 * skipped-capacity and removes the staging payload.
 */
export async function prepareCacheSnapshot({
  kind,
  key,
  workspace = process.cwd(),
  env = process.env,
  log = () => {},
  capacityImpl = filesystemCapacity,
  run = spawnSync,
}) {
  const policy = cachePolicy(kind)
  validateCacheKey(kind, key, env)
  if (policy.writerScope === 'main')
    assertMainPush(env, kind)
  const { payloadRoot, stagingRoot } = snapshotPaths(kind, workspace)
  assertInsideStaging(payloadRoot, kind, workspace)
  const roots = activeRootsFor(kind, env, workspace)
  let before = null
  let selections = []
  if (policy.population === 'activate-then-produce') {
    removePath(payloadRoot)
    mkdirSync(payloadRoot, { recursive: true })
    if (policy.trim === 'rust') {
      const trimmed = trimRustSnapshot({
        kind,
        workspace,
        env,
        payloadRoot,
        log,
      })
      before = { bytes: trimmed.bytesBeforeTrim, files: trimmed.filesBeforeTrim }
      selections = trimmed.selections
    }
    if (policy.trim === 'apt') {
      const trimmed = trimAptArchives({ kind, workspace, env, log, run })
      selections = trimmed.selections
      before = { bytes: trimmed.bytesBeforeTrim, files: trimmed.filesBeforeTrim }
    }
    let bytes = 0
    let files = 0
    for (const root of roots) {
      if (policy.trim === 'apt' || (policy.trim === 'rust' && root.name !== 'target'))
        continue
      if (!pathExists(root.active)) {
        if (root.required)
          throw new Error(`The ${kind} required output ${root.active} is absent.`)
        continue
      }
      const stat = lstatSync(root.active)
      if ((root.type === 'tree' && !stat.isDirectory()) || (root.type === 'file' && !stat.isFile()))
        throw new Error(`The ${kind} active root ${root.name} has the wrong type.`)
      const measured = measureCacheTree(root.active)
      bytes += measured.bytes
      files += measured.files
      selections.push({ source: root.active, destination: root.payload })
    }
    before ??= { bytes, files }
  }
  else {
    const validation = validatePayloadRoots(kind, roots)
    if (!validation.valid)
      throw new Error(validation.reason)
  }
  const selected = selections.length === 0
    ? measureCacheTree(payloadRoot)
    : selections.reduce((total, entry) => {
        const measured = measureCacheTree(entry.source)
        return { bytes: total.bytes + measured.bytes, files: total.files + measured.files, directories: total.directories + measured.directories }
      }, { bytes: 0, files: 0, directories: 0 })
  const capacity = capacityImpl(stagingRoot)
  const requiredBytes = selected.bytes * CAPACITY_BYTES_MULTIPLIER + CAPACITY_BYTES_HEADROOM
  const requiredInodes = (selected.files + selected.directories) * CAPACITY_INODE_MULTIPLIER + roots.length + selections.length * 3
  const report = {
    'bytes-before-trim': before?.bytes ?? selected.bytes,
    'files-before-trim': before?.files ?? selected.files,
    'bytes-after-trim': selected.bytes,
    'files-after-trim': selected.files,
    'selected-bytes': selected.bytes,
    'selected-files': selected.files,
    'free-bytes': capacity.freeBytes,
    'available-inodes': capacity.availableInodes,
    'compressed-size': UNAVAILABLE,
  }
  if ((capacity.freeBytes !== null && capacity.freeBytes < requiredBytes)
    || (capacity.availableInodes !== null && capacity.availableInodes < requiredInodes)) {
    log(`Skipping the optional ${kind} cache publication: ${formatCount(capacity.freeBytes)} free bytes and ${formatCount(capacity.availableInodes)} available inodes cannot hold the ${selected.bytes}-byte snapshot with archive headroom.`)
    removePath(payloadRoot)
    return { result: 'skipped-capacity', archivePath: '', report }
  }
  for (const root of roots) {
    if (root.type === 'tree' && root.required)
      mkdirSync(root.payload, { recursive: true })
  }
  for (const entry of selections) {
    mkdirSync(dirname(entry.destination), { recursive: true })
    copyEntryPreserving(entry.source, entry.destination)
  }
  const validation = validatePayloadRoots(kind, roots)
  if (!validation.valid)
    throw new Error(validation.reason)
  log(`Prepared the ${kind} snapshot: ${selected.files} files and ${selected.bytes} bytes selected, ${formatCount(capacity.freeBytes)} free bytes remain.`)
  return { result: 'ready', archivePath: payloadRoot, report }
}

/**
 * Remove the staging root of one kind. The root comes from the policy
 * registry, so an active root can never be a deletion target.
 */
export function cleanupSnapshotStaging({ kind, workspace = process.cwd() }) {
  const { stagingRoot } = snapshotPaths(kind, workspace)
  assertInsideStaging(stagingRoot, kind, workspace)
  removePath(stagingRoot)
}

// --- Command line ----------------------------------------------------------

function parseArgs(argv) {
  const args = {}
  const positional = []
  for (let index = 0; index < argv.length; index++) {
    const arg = argv[index]
    if (!arg.startsWith('--')) {
      positional.push(arg)
      continue
    }
    const name = arg.slice(2)
    const next = argv[index + 1]
    if (next === undefined || next.startsWith('--')) {
      args[name] = true
      continue
    }
    args[name] = next
    index++
  }
  return { args, positional }
}

function writeOutputs(entries) {
  const text = Object.entries(entries)
    .map(([name, value]) => `${name}=${value}\n`)
    .join('')
  const output = process.env.GITHUB_OUTPUT
  if (output)
    appendFileSync(output, text)
  process.stdout.write(text)
}

async function main(argv) {
  const { args, positional } = parseArgs(argv)
  const command = positional[0]
  const log = message => process.stdout.write(`${message}\n`)
  const kind = args.kind
  if (typeof kind !== 'string' || kind === '')
    throw new Error('Every cache command needs a --kind from the policy registry.')
  switch (command) {
    case 'activate': {
      const activated = await activateCacheSnapshot({
        kind,
        key: args.key,
        writer: args.writer === 'true',
        exactHit: args['exact-hit'] === 'true',
        matchedKey: args['matched-key'] || null,
        log,
      })
      writeOutputs({
        'outcome': activated.outcome,
        'cache-hit': activated.cacheHit,
        'save-required': activated.saveRequired,
        'active-path': activated.activePath,
      })
      return
    }
    case 'prepare': {
      const prepared = await prepareCacheSnapshot({ kind, key: args.key, log })
      writeOutputs({
        'result': prepared.result,
        'archive-path': prepared.archivePath,
        ...Object.fromEntries(Object.entries(prepared.report).map(([name, value]) => [
          name,
          formatCount(value),
        ])),
      })
      return
    }
    case 'cleanup': {
      cleanupSnapshotStaging({
        kind,
        workspace: process.cwd(),
      })
      return
    }
    case 'publish-dependency': {
      publishDependencySnapshot({ kind, key: args.key, log })
      return
    }
    case 'payload-path': {
      process.stdout.write(`${args.root
        ? rootPayloadPath(kind, args.root)
        : snapshotPaths(kind).payloadRoot}\n`)
      return
    }
    case 'active-path': {
      process.stdout.write(`${rootActivePath(kind, args.root)}\n`)
      return
    }
    case 'prefix': {
      process.stdout.write(`${partitionPrefix(kind)}\n`)
      return
    }
    case 'key': {
      process.stdout.write(`${cacheKey(kind, { hash: args.hash, week: args.week })}\n`)
      return
    }
    case 'restore-keys': {
      process.stdout.write(`${restoreKeys(kind, { hash: args.hash }) ?? ''}\n`)
      return
    }
    case 'validate-key': {
      validateCacheKey(kind, args.key)
      return
    }
    default:
      throw new Error(`Unknown cache command: ${command}. Use activate, prepare, cleanup, publish-dependency, payload-path, active-path, prefix, key, restore-keys, or validate-key.`)
  }
}

const isMain = process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)

if (isMain) {
  main(process.argv.slice(2)).catch((error) => {
    console.error(`Cache snapshot command failed: ${error.message}`)
    process.exitCode = 1
  })
}
