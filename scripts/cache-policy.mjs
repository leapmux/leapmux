// One registry of every project-owned cache. Each policy states what its
// archive holds, where its active data lives, how its key is composed, and
// which jobs may publish it. The snapshot implementation in
// scripts/cache-snapshot.mjs resolves every path and rule through this
// registry, so no workflow text carries a deletion root or a cache layout.
//
// Every key embeds CACHE_SNAPSHOT_VERSION inside its partition prefix. A new
// generation therefore never restores an archive that an older retention
// rule produced, and the pruner cannot list one.
import { createHash } from 'node:crypto'
import { join } from 'node:path'
import process from 'node:process'

export const CACHE_SNAPSHOT_VERSION = 2

// The staging layout beneath the workspace. actions/cache archives the
// payload directory of one kind; cleanup removes the staging root of that
// kind and nothing else.
const SNAPSHOT_STAGING_SUBPATH = ['.tmp', 'ci-cache', 'snapshots']

const WEEK_PATTERN = '\\d{4}-W(?:0[1-9]|[1-4]\\d|5[0-3])'
const WEEK_REGEXP = new RegExp(`^${WEEK_PATTERN}$`)
const HASH_SEGMENT_PATTERN = /^[a-f0-9]{64}(?:-[a-f0-9]{64})*$/
const KEY_SAFE_PATTERN = /^[\w.-]+$/

// The week-53 leap rule of ISO 8601: week 53 exists only when the year
// starts on a Thursday, or on a Wednesday of a leap year. Every other week
// number of the pattern exists in every year.
export function isValidIsoWeek(week) {
  if (typeof week !== 'string' || !WEEK_REGEXP.test(week))
    return false
  if (!week.endsWith('-W53'))
    return true
  const year = Number(week.slice(0, 4))
  const firstDay = new Date(`${year}-01-01T00:00:00Z`).getUTCDay()
  const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0)
  return firstDay === 4 || (firstDay === 3 && leap)
}

function requireEnv(env, name) {
  const value = env[name]
  if (typeof value !== 'string' || value === '')
    throw new Error(`The cache policy for this runner requires the ${name} environment variable.`)
  return value
}

function optionalEnv(env, name) {
  const value = env[name]
  return typeof value === 'string' && value !== ''
    ? value
    : null
}

// The runner environment decides the defaults, never the platform of the
// process that resolves them: a helper may compute a Windows runner's roots
// while it runs on macOS.
function homeDir(env) {
  if (requireEnv(env, 'RUNNER_OS') === 'Windows')
    return requireEnv(env, 'USERPROFILE')
  return requireEnv(env, 'HOME')
}

function goModCache(env) {
  return env.GOMODCACHE || join(homeDir(env), 'go', 'pkg', 'mod')
}

function bunCache(env) {
  return env.BUN_INSTALL_CACHE_DIR || join(homeDir(env), '.bun', 'install', 'cache')
}

function cargoHome(env) {
  return env.CARGO_HOME || join(homeDir(env), '.cargo')
}

function tauriCache(env) {
  if (requireEnv(env, 'RUNNER_OS') === 'Windows')
    return join(homeDir(env), 'AppData', 'Local', 'tauri')
  return join(homeDir(env), '.cache', 'tauri')
}

function spinnersClone(env) {
  return join(homeDir(env), '.cache', 'awesome-claude-spinners')
}

function dockerArch(env) {
  const arch = requireEnv(env, 'RUNNER_ARCH')
  if (arch === 'X64')
    return 'amd64'
  if (arch === 'ARM64')
    return 'arm64'
  throw new Error(`The Docker Go cache does not support RUNNER_ARCH=${arch}.`)
}

function workspaceRoot(env, workspace) {
  return env.LEAPMUX_CACHE_WORKSPACE || workspace
}

// The sha256 that Task records for a task name, which names the artifact
// record file under .task/artifacts. Mirrors scripts/task-state.mjs.
export function taskArtifactRecordName(task) {
  return `${createHash('sha256').update(task).digest('hex')}.sha256`
}

// Active-root resolvers. A resolver returns null when the root does not
// apply to the current runner (for example the Tauri tool cache on macOS),
// and the snapshot implementation then skips that root.
const activeRoots = {
  goModCache: env => goModCache(env),
  bunCache: env => bunCache(env),
  cargoHome: env => cargoHome(env),
  cargoRegistrySrc: env => join(cargoHome(env), 'registry', 'src'),
  cargoGitCheckouts: env => join(cargoHome(env), 'git', 'checkouts'),
  goBuildCache: env => optionalEnv(env, 'GOCACHE'),
  goLintCache: env => optionalEnv(env, 'GOLANGCI_LINT_CACHE'),
  rustTarget: (env, workspace) => join(workspaceRoot(env, workspace), 'desktop', 'rust', 'target'),
  dockerGoBuild: env => join(requireEnv(env, 'RUNNER_TEMP'), 'docker-go-build'),
  aptArchives: env => optionalEnv(env, 'LEAPMUX_APT_ARCHIVE_DIR') ?? '/var/cache/apt/archives',
  tauriCache: env => requireEnv(env, 'RUNNER_OS') === 'macOS'
    ? null
    : tauriCache(env),
  spinnersSubtree: env => join(spinnersClone(env), 'spinners'),
  renovateRe2Node: (env, workspace) => join(workspaceRoot(env, workspace), '.github', 'renovate', 'node_modules', 're2', 'build', 'Release', 're2.node'),
  renovateTaskChecksum: (env, workspace) => join(workspaceRoot(env, workspace), '.task', 'checksum', 'build-renovate-re2'),
  renovateTaskRecord: (env, workspace) => join(workspaceRoot(env, workspace), '.task', 'artifacts', taskArtifactRecordName('build-renovate-re2')),
}

// The prefix builders return the partition that this generation owns. Each
// embeds CACHE_SNAPSHOT_VERSION, so no prefix of this generation matches an
// archive of a previous one.
const prefixBuilders = {
  'go-modules': env => `go-modules-${CACHE_SNAPSHOT_VERSION}-${requireEnv(env, 'RUNNER_OS')}-`,
  'site-go-modules': env => `site-go-modules-${CACHE_SNAPSHOT_VERSION}-${requireEnv(env, 'RUNNER_OS')}-`,
  'bun-packages': env => `bun-packages-${CACHE_SNAPSHOT_VERSION}-${requireEnv(env, 'RUNNER_OS')}-${requireEnv(env, 'RUNNER_ARCH')}-`,
  'cargo-downloads': env => `cargo-downloads-${CACHE_SNAPSHOT_VERSION}-${requireEnv(env, 'RUNNER_OS')}-`,
  'native-go': env => `go-build-${CACHE_SNAPSHOT_VERSION}-${requireEnv(env, 'RUNNER_OS')}-${requireEnv(env, 'RUNNER_ARCH')}-${requireEnv(env, 'CACHE_IMAGE')}-go${requireEnv(env, 'GOLANG_VERSION')}-`,
  'native-rust': env => `rust-build-${CACHE_SNAPSHOT_VERSION}-${requireEnv(env, 'RUNNER_OS')}-${requireEnv(env, 'RUNNER_ARCH')}-${requireEnv(env, 'CACHE_IMAGE')}-rust${requireEnv(env, 'RUST_VERSION')}-`,
  'docker-go': env => `docker-go-build-${CACHE_SNAPSHOT_VERSION}-${dockerArch(env)}-go${requireEnv(env, 'GOLANG_VERSION')}-`,
  'site-go': env => `site-go-${CACHE_SNAPSHOT_VERSION}-${requireEnv(env, 'RUNNER_OS')}-go${requireEnv(env, 'GOLANG_VERSION')}-`,
  'apt-packages': env => `apt-${CACHE_SNAPSHOT_VERSION}-${requireEnv(env, 'RUNNER_OS')}-${requireEnv(env, 'RUNNER_ARCH')}-`,
  'tauri-tools': env => `tauri-${CACHE_SNAPSHOT_VERSION}-${requireEnv(env, 'RUNNER_OS')}-${requireEnv(env, 'RUNNER_ARCH')}-`,
  'spinners': env => `spinners-${CACHE_SNAPSHOT_VERSION}-${requireEnv(env, 'RUNNER_OS')}-`,
  'renovate-re2': env => `renovate-re2-${CACHE_SNAPSHOT_VERSION}-${requireEnv(env, 'RUNNER_OS')}-${requireEnv(env, 'RUNNER_ARCH')}-${requireEnv(env, 'CACHE_IMAGE')}-node24-bun${requireEnv(env, 'BUN_VERSION')}-task${requireEnv(env, 'TASK_VERSION')}-`,
}

// How a kind publishes its archive:
// - 'isolated-download': the population downloads into the empty payload
//   itself, so the archive can only hold files of the current declarations.
// - 'activate-then-produce': restoration activates a snapshot or starts from
//   empty owned roots, the workload produces into the active roots, and
//   preparation copies the trimmed selection into the payload.
const POPULATION_ISOLATED_DOWNLOAD = 'isolated-download'
const POPULATION_ACTIVATE_THEN_PRODUCE = 'activate-then-produce'

// Trim rules that preparation executes in place before it selects the
// payload. Every kind selects its files before preparation checks capacity.
const TRIM_NONE = 'none'
const TRIM_RUST = 'rust'
const TRIM_APT = 'apt'

// writerScope 'main' refuses preparation when the job does not run on a
// main-branch push or manual dispatch, which keeps compiled-cache
// publication on the jobs that completed full validation.
const WRITER_SCOPE_MAIN = 'main'
const WRITER_SCOPE_ANY = 'any'

const CACHE_POLICIES = {
  'go-modules': {
    kind: 'go-modules',
    category: 'dependency',
    description: 'Workspace and isolated tool module downloads, populated by scripts/download-go-modules.mjs.',
    population: POPULATION_ISOLATED_DOWNLOAD,
    trim: TRIM_NONE,
    writerScope: WRITER_SCOPE_ANY,
    writers: 'Any job that downloads the workspace modules.',
    keyedByWeek: false,
    hashSegment: 'required',
    fallback: 'none',
    roots: [
      {
        name: 'gomod',
        type: 'tree',
        required: true,
        mode: 'merge',
        clearOnWriterMiss: false,
        active: activeRoots.goModCache,
      },
    ],
  },
  'site-go-modules': {
    kind: 'site-go-modules',
    category: 'dependency',
    description: 'Site module downloads, populated by `GOWORK=off go mod download` from site/.',
    population: POPULATION_ISOLATED_DOWNLOAD,
    trim: TRIM_NONE,
    writerScope: WRITER_SCOPE_ANY,
    writers: 'The site publish job that downloads the site modules.',
    keyedByWeek: false,
    hashSegment: 'required',
    fallback: 'none',
    roots: [
      {
        name: 'gomod',
        type: 'tree',
        required: true,
        mode: 'merge',
        clearOnWriterMiss: false,
        active: activeRoots.goModCache,
      },
    ],
  },
  'bun-packages': {
    kind: 'bun-packages',
    category: 'dependency',
    description: 'Package downloads populated from frontend/bun.lock with `bun install --frozen-lockfile --ignore-scripts`.',
    population: POPULATION_ISOLATED_DOWNLOAD,
    trim: TRIM_NONE,
    writerScope: WRITER_SCOPE_ANY,
    writers: 'Any job that installs the frontend packages.',
    keyedByWeek: false,
    hashSegment: 'required',
    fallback: 'none',
    roots: [
      {
        name: 'bun-cache',
        type: 'tree',
        required: true,
        mode: 'merge',
        clearOnWriterMiss: false,
        active: activeRoots.bunCache,
      },
    ],
  },
  'cargo-downloads': {
    kind: 'cargo-downloads',
    category: 'dependency',
    description: 'The current Cargo.lock registry downloads, the sparse index, and the Git database of the desktop workspace.',
    population: POPULATION_ISOLATED_DOWNLOAD,
    trim: TRIM_NONE,
    writerScope: WRITER_SCOPE_ANY,
    writers: 'Any job that fetches the desktop Rust dependencies.',
    keyedByWeek: false,
    hashSegment: 'required',
    fallback: 'none',
    roots: [
      {
        name: 'cargo-home',
        type: 'tree',
        required: true,
        mode: 'merge',
        clearOnWriterMiss: false,
        active: activeRoots.cargoHome,
      },
    ],
  },
  'native-go': {
    kind: 'native-go',
    category: 'compiled',
    description: 'Go build outputs and golangci-lint results from a complete native validation of the current workspace.',
    population: POPULATION_ACTIVATE_THEN_PRODUCE,
    trim: TRIM_NONE,
    writerScope: WRITER_SCOPE_MAIN,
    writers: 'Only the CI platform jobs on a main-branch push or dispatch, after all required validation.',
    keyedByWeek: true,
    hashSegment: 'required',
    fallback: 'prefix',
    roots: [
      {
        name: 'gocache',
        type: 'tree',
        required: true,
        mode: 'replace',
        clearOnWriterMiss: true,
        active: activeRoots.goBuildCache,
      },
      {
        name: 'lint-cache',
        type: 'tree',
        required: false,
        mode: 'replace',
        clearOnWriterMiss: true,
        active: activeRoots.goLintCache,
      },
    ],
  },
  'native-rust': {
    kind: 'native-rust',
    category: 'compiled',
    description: 'Reusable Rust dependency outputs for every profile and feature configuration of the producer, with the extracted -sys sources and Git checkouts that their reuse requires.',
    population: POPULATION_ACTIVATE_THEN_PRODUCE,
    trim: TRIM_RUST,
    writerScope: WRITER_SCOPE_MAIN,
    writers: 'Only the CI platform jobs on a main-branch push or dispatch, after all required validation.',
    keyedByWeek: false,
    // The Rust key carries no weekly epoch: the trimming rule restricts its
    // growth, and a weekly rotation would cold-build every dependency tree
    // each Monday as the combined upstream cache did not.
    hashSegment: 'required',
    fallback: 'prefix',
    roots: [
      {
        name: 'target',
        type: 'tree',
        required: true,
        mode: 'replace',
        clearOnWriterMiss: true,
        active: activeRoots.rustTarget,
      },
      {
        name: 'registry-src',
        type: 'tree',
        required: false,
        mode: 'merge',
        clearOnWriterMiss: true,
        active: activeRoots.cargoRegistrySrc,
      },
      {
        name: 'git-checkouts',
        type: 'tree',
        required: false,
        mode: 'merge',
        clearOnWriterMiss: true,
        active: activeRoots.cargoGitCheckouts,
      },
    ],
  },
  'docker-go': {
    kind: 'docker-go',
    category: 'compiled',
    description: 'Go build outputs exported from the Go cache mount of the current Docker build, extracted after the image passes its runtime tests.',
    population: POPULATION_ACTIVATE_THEN_PRODUCE,
    trim: TRIM_NONE,
    writerScope: WRITER_SCOPE_MAIN,
    writers: 'Only the alpine-variant main-branch Docker writer of one architecture, after the export succeeds.',
    keyedByWeek: true,
    hashSegment: 'required',
    fallback: 'prefix',
    roots: [
      {
        name: 'go-build',
        type: 'tree',
        required: true,
        mode: 'replace',
        clearOnWriterMiss: true,
        active: activeRoots.dockerGoBuild,
      },
    ],
  },
  'site-go': {
    kind: 'site-go',
    category: 'compiled',
    description: 'Site Go build outputs from the current site build.',
    population: POPULATION_ACTIVATE_THEN_PRODUCE,
    trim: TRIM_NONE,
    writerScope: WRITER_SCOPE_MAIN,
    writers: 'The site publish job on main, after its build succeeds.',
    keyedByWeek: true,
    hashSegment: 'required',
    fallback: 'prefix',
    roots: [
      {
        name: 'gocache',
        type: 'tree',
        required: true,
        mode: 'replace',
        clearOnWriterMiss: true,
        active: activeRoots.goBuildCache,
      },
    ],
  },
  'apt-packages': {
    kind: 'apt-packages',
    category: 'tool',
    description: 'Downloaded prerequisite packages whose package, version, and architecture match the successful installation.',
    population: POPULATION_ACTIVATE_THEN_PRODUCE,
    trim: TRIM_APT,
    writerScope: WRITER_SCOPE_ANY,
    writers: 'Any Linux desktop job after a successful prerequisite installation and trim.',
    keyedByWeek: true,
    hashSegment: 'required',
    // Every job that restores also publishes after its installation, so the
    // kind has no reader role and permits no fallback.
    fallback: 'none',
    roots: [
      {
        name: 'apt-archives',
        type: 'tree',
        required: true,
        mode: 'merge',
        clearOnWriterMiss: false,
        active: activeRoots.aptArchives,
      },
    ],
  },
  'tauri-tools': {
    kind: 'tauri-tools',
    category: 'tool',
    description: 'Tool downloads and patched files produced by a complete packaging build: the Linux wrapper, the extracted tree, the .orig marker, and the patched plugins.',
    population: POPULATION_ACTIVATE_THEN_PRODUCE,
    trim: TRIM_NONE,
    writerScope: WRITER_SCOPE_ANY,
    writers: 'Every non-macOS job after a successful complete packaging build.',
    keyedByWeek: true,
    hashSegment: 'required',
    // Every job that restores also publishes after its packaging build, so
    // the kind has no reader role and permits no fallback.
    fallback: 'none',
    roots: [
      {
        name: 'tauri',
        type: 'tree',
        required: true,
        mode: 'replace',
        clearOnWriterMiss: true,
        active: activeRoots.tauriCache,
      },
    ],
  },
  'spinners': {
    kind: 'spinners',
    category: 'tool',
    description: 'The spinner source subtree that generation consumes, cloned by `task generate-spinners`.',
    population: POPULATION_ACTIVATE_THEN_PRODUCE,
    trim: TRIM_NONE,
    writerScope: WRITER_SCOPE_ANY,
    writers: 'Every job after `task generate-spinners` completes.',
    keyedByWeek: true,
    hashSegment: 'none',
    fallback: 'none',
    roots: [
      {
        name: 'spinners',
        type: 'tree',
        required: true,
        requiredNonEmpty: true,
        mode: 'merge',
        clearOnWriterMiss: true,
        active: activeRoots.spinnersSubtree,
      },
    ],
  },
  'renovate-re2': {
    kind: 'renovate-re2',
    category: 'tool',
    description: 'The compiled Renovate RE2 engine with the Task checksum and artifact record that accept it.',
    population: POPULATION_ACTIVATE_THEN_PRODUCE,
    trim: TRIM_NONE,
    writerScope: WRITER_SCOPE_MAIN,
    writers: 'The Linux amd64 job on a main-branch push, after Renovate extraction validates the engine.',
    keyedByWeek: true,
    hashSegment: 'required',
    fallback: 'prefix-first-hash',
    roots: [
      {
        name: 're2-node',
        type: 'file',
        required: true,
        mode: 'replace',
        clearOnWriterMiss: false,
        active: activeRoots.renovateRe2Node,
      },
      {
        name: 'task-checksum',
        type: 'file',
        required: true,
        mode: 'replace',
        clearOnWriterMiss: false,
        active: activeRoots.renovateTaskChecksum,
      },
      {
        name: 'task-record',
        type: 'file',
        required: true,
        mode: 'replace',
        clearOnWriterMiss: false,
        active: activeRoots.renovateTaskRecord,
      },
    ],
  },
}

const POLICY_KINDS = Object.freeze(Object.keys(CACHE_POLICIES))

/** Validate one policy definition shape before any filesystem use. */
export function validatePolicy(policy) {
  const problems = []
  for (const field of [
    'kind',
    'category',
    'description',
    'population',
    'trim',
    'writerScope',
    'writers',
  ]) {
    if (typeof policy[field] !== 'string' || policy[field] === '')
      problems.push(`${field} must be a non-empty string`)
  }
  if (!['dependency', 'compiled', 'tool'].includes(policy.category))
    problems.push(`unknown category ${policy.category}`)
  if (!POLICY_KINDS.includes(policy.kind))
    problems.push(`unknown kind ${policy.kind}`)
  if (!Array.isArray(policy.roots) || policy.roots.length === 0)
    problems.push('roots must be a non-empty array')
  const names = new Set()
  for (const root of policy.roots ?? []) {
    for (const field of ['name', 'type', 'mode']) {
      if (typeof root[field] !== 'string' || root[field] === '')
        problems.push(`root ${root.name ?? '?'}: ${field} must be a non-empty string`)
    }
    if (typeof root.active !== 'function')
      problems.push(`root ${root.name}: active must be a resolver function`)
    if (names.has(root.name))
      problems.push(`duplicate root name ${root.name}`)
    names.add(root.name)
    if (root.required && !['tree', 'file'].includes(root.type))
      problems.push(`root ${root.name}: required roots need a tree or file type`)
    if (root.requiredNonEmpty && !root.required)
      problems.push(`root ${root.name}: requiredNonEmpty needs required`)
  }
  if (policy.keyedByWeek !== true && policy.keyedByWeek !== false)
    problems.push('keyedByWeek must be a boolean')
  if (!['required', 'none'].includes(policy.hashSegment))
    problems.push(`unknown hashSegment ${policy.hashSegment}`)
  if (!['none', 'prefix', 'prefix-first-hash'].includes(policy.fallback))
    problems.push(`unknown fallback ${policy.fallback}`)
  if (policy.hashSegment === 'none' && policy.fallback !== 'none')
    problems.push('a kind without a hash segment cannot fall back on one')
  if (!policy.keyedByWeek && policy.hashSegment === 'none')
    problems.push('a dependency key without a week needs a hash segment')
  if (problems.length > 0)
    throw new Error(`The cache policy ${policy.kind || '(missing kind)'} is invalid: ${problems.join('; ')}`)
  return policy
}

for (const policy of Object.values(CACHE_POLICIES))
  validatePolicy(policy)

/** Return the policy of one cache kind, or throw for an unknown kind. */
export function cachePolicy(kind) {
  if (typeof kind !== 'string' || !Object.hasOwn(CACHE_POLICIES, kind))
    throw new Error(`Unknown cache kind: ${String(kind)}. Known kinds: ${POLICY_KINDS.join(', ')}.`)
  return CACHE_POLICIES[kind]
}

/** Return every known cache kind. */
export function cacheKinds() {
  return [...POLICY_KINDS]
}

/** The exact partition prefix that this generation of a kind owns. */
export function partitionPrefix(kind, env = process.env) {
  // Validate the kind even though the builder lookup below covers it: an
  // unknown kind must fail with the registry's message.
  cachePolicy(kind)
  const prefix = prefixBuilders[kind](env)
  if (!prefix.includes(`-${CACHE_SNAPSHOT_VERSION}-`))
    throw new Error(`The ${kind} prefix must embed the snapshot generation.`)
  if (!prefix.endsWith('-'))
    throw new Error(`The ${kind} prefix must end with a dash.`)
  if (!KEY_SAFE_PATTERN.test(prefix))
    throw new Error(`The ${kind} prefix must stay safe for cache keys.`)
  return prefix
}

/**
 * The exact archive key of a kind for the given content hash and UTC week.
 * Dependency kinds carry no week: their archive is the current declaration
 * set and nothing else.
 */
export function cacheKey(kind, { hash, week } = {}, env = process.env) {
  const policy = cachePolicy(kind)
  const prefix = partitionPrefix(kind, env)
  if (policy.hashSegment === 'required') {
    if (typeof hash !== 'string' || !HASH_SEGMENT_PATTERN.test(hash))
      throw new Error(`The ${kind} cache key needs a sha256 content hash.`)
  }
  else if (hash !== undefined) {
    throw new Error(`The ${kind} cache key carries no hash segment.`)
  }
  if (!policy.keyedByWeek) {
    if (week !== undefined)
      throw new Error(`The ${kind} cache key carries no weekly epoch.`)
    return `${prefix}${hash}`
  }
  if (!isValidIsoWeek(week))
    throw new Error(`The ${kind} cache key needs a valid ISO UTC week, got ${week}.`)
  return hash === undefined
    ? `${prefix}${week}`
    : `${prefix}${hash}-${week}`
}

/**
 * The permitted fallback prefixes that a READER may restore. A writer must
 * not pass any of them to its restore: a writer that misses its exact key
 * starts from an empty directory owned by this cache.
 */
export function restoreKeys(kind, { hash } = {}, env = process.env) {
  const policy = cachePolicy(kind)
  if (policy.fallback === 'none')
    return null
  const prefix = partitionPrefix(kind, env)
  if (policy.fallback === 'prefix')
    return prefix
  // 'prefix-first-hash': the fallback may cross the later recipe hashes but
  // not the dependency hash, as the Renovate RE2 engine requires.
  if (typeof hash !== 'string')
    throw new TypeError(`The ${kind} fallback needs the dependency hash.`)
  const first = hash.split('-')[0]
  if (!/^[a-f0-9]{64}$/.test(first))
    throw new Error(`The ${kind} fallback needs a sha256 dependency hash.`)
  return `${prefix}${first}-`
}

/**
 * Validate that `key` is an exact key of this generation for `kind`.
 * Returns { hash, week }; throws when the key belongs to another partition,
 * another generation, or a malformed week.
 */
export function validateCacheKey(kind, key, env = process.env) {
  const policy = cachePolicy(kind)
  if (typeof key !== 'string' || !KEY_SAFE_PATTERN.test(key))
    throw new Error(`The ${kind} cache key is malformed.`)
  const prefix = partitionPrefix(kind, env)
  if (!key.startsWith(prefix))
    throw new Error(`The ${kind} cache key does not belong to the current generation partition ${prefix}.`)
  const rest = key.slice(prefix.length)
  if (!policy.keyedByWeek) {
    if (!HASH_SEGMENT_PATTERN.test(rest))
      throw new Error(`The ${kind} cache key must end with a sha256 content hash.`)
    return { hash: rest, week: null }
  }
  if (policy.hashSegment === 'none') {
    if (!isValidIsoWeek(rest))
      throw new Error(`The ${kind} cache key must end with a valid ISO UTC week.`)
    return { hash: null, week: rest }
  }
  const match = new RegExp(`^(${HASH_SEGMENT_PATTERN.source.slice(1, -1)})-(${WEEK_PATTERN})$`).exec(rest)
  if (!match)
    throw new Error(`The ${kind} cache key must end with a sha256 hash and a valid ISO UTC week.`)
  if (!isValidIsoWeek(match[2]))
    throw new Error(`The ${kind} cache key carries an invalid ISO UTC week.`)
  return { hash: match[1], week: match[2] }
}

/**
 * The staging directories of one kind beneath the workspace. cleanup removes
 * the staging root; the restore and save transports see the payload root.
 */
export function snapshotPaths(kind, workspace = process.cwd()) {
  cachePolicy(kind)
  if (typeof workspace !== 'string' || workspace === '')
    throw new Error('The cache snapshot needs a workspace directory.')
  const stagingRoot = join(workspace, ...SNAPSHOT_STAGING_SUBPATH, kind)
  return {
    stagingRoot,
    payloadRoot: join(stagingRoot, 'payload'),
  }
}

/**
 * Resolve the roots of a kind for the current environment. Skips roots whose
 * resolver returns null for this runner. Paths outside the workspace stay
 * absolute; workspace-relative policy roots resolve against `workspace`.
 */
export function activeRootsFor(kind, env = process.env, workspace = process.cwd()) {
  const policy = cachePolicy(kind)
  const { payloadRoot } = snapshotPaths(kind, workspace)
  const roots = []
  for (const root of policy.roots) {
    const active = root.active(env, workspace)
    if (active === null)
      continue
    roots.push(Object.freeze({
      ...root,
      active,
      payload: join(payloadRoot, root.name),
    }))
  }
  const requiredNames = policy.roots.filter(root => root.required).map(root => root.name)
  for (const name of requiredNames) {
    if (!roots.some(root => root.name === name))
      throw new Error(`The ${kind} cache root ${name} does not apply to this runner, but the policy requires it.`)
  }
  return roots
}

/** The payload directory of one root, for population or transport wiring. */
export function rootPayloadPath(kind, rootName, env = process.env, workspace = process.cwd()) {
  const root = activeRootsFor(kind, env, workspace).find(entry => entry.name === rootName)
  if (!root)
    throw new Error(`The ${kind} cache has no applicable root ${rootName}.`)
  return root.payload
}

/** The active directory of one root, for callers that consume it directly. */
export function rootActivePath(kind, rootName, env = process.env, workspace = process.cwd()) {
  const root = activeRootsFor(kind, env, workspace).find(entry => entry.name === rootName)
  if (!root)
    throw new Error(`The ${kind} cache has no applicable root ${rootName}.`)
  return root.active
}

// Tool installer caches are owned by their installers, never by project
// snapshots. The inventory guard uses these classes; nothing here may enter
// an archive.
export const INSTALLER_OWNED_CACHES = [
  {
    mechanism: 'oven-sh/setup-bun',
    owner: 'the Bun installer',
    contents: 'the requested Bun version installation',
  },
  {
    mechanism: 'rustup (RUSTUP_HOME)',
    owner: 'the Rust toolchain installer',
    contents: 'the installed toolchains; excluded from project snapshots',
  },
  {
    mechanism: 'actions/setup-node',
    owner: 'the Node.js setup action',
    contents: 'must keep its package cache disabled',
  },
  {
    mechanism: 'actions/setup-go',
    owner: 'the Go setup action',
    contents: 'must keep its combined module and build cache disabled',
  },
]

/** The cache-producing mechanisms that every workflow must classify. */
export const CLASSIFIED_CACHE_MECHANISMS = [
  { uses: './.github/actions/build-output-cache', owner: 'the snapshot restore action' },
  { uses: './.github/actions/save-cache-snapshot', owner: 'the snapshot save action' },
  { uses: './.github/actions/dependency-caches', owner: 'the dependency snapshot action' },
]

// The action files whose steps may talk to the cache transport directly.
const CACHE_TRANSPORT_ACTION_FILES = [
  '.github/actions/build-output-cache/action.yaml',
  '.github/actions/save-cache-snapshot/action.yaml',
  '.github/actions/dependency-caches/action.yaml',
]

function isTransportActionFile(source) {
  return CACHE_TRANSPORT_ACTION_FILES.includes(String(source).replace(/^\.\//, ''))
}

/**
 * Scan steps for cache mechanisms that no policy classifies. Returns one
 * violation string per unclassified mechanism; an empty list means every
 * cache-producing mechanism in the input flows through the snapshot actions
 * or keeps its installer-owned cache disabled.
 */
export function scanCacheMechanisms(entries) {
  const violations = []
  for (const { source, steps } of entries) {
    const inTransportAction = isTransportActionFile(source)
    for (const { jobName, step } of steps) {
      const uses = String(step.uses ?? '')
      const location = `${source}${jobName
        ? ` job ${jobName}`
        : ''}: ${step.name ?? uses}`
      if (uses.startsWith('actions/cache')) {
        if (!inTransportAction)
          violations.push(`${location} talks to the cache transport directly; the snapshot actions own it.`)
        continue
      }
      if (uses.startsWith('Swatinem/rust-cache')) {
        violations.push(`${location} uses the combined upstream Rust cache; the native-rust snapshot policy replaced it.`)
        continue
      }
      // setup-go enables its combined cache whenever its input is absent,
      // so only an explicit 'false' keeps the mechanism off.
      if (uses.startsWith('actions/setup-go') && String(step.with?.cache ?? 'true') !== 'false') {
        violations.push(`${location} enables setup-go's combined module and build cache; the go-modules and compiled snapshot policies replace it.`)
        continue
      }
      for (const key of ['cache', 'caching']) {
        const value = step.with?.[key]
        if (value !== undefined && String(value) !== 'false' && !uses.startsWith('actions/setup-go'))
          violations.push(`${location} enables the ${key} input of ${uses}; no policy classifies this mechanism.`)
      }
    }
  }
  return violations
}

/**
 * The exact partitions that the remote replacement pruner may manage: the
 * compiled Go output partitions of this generation. Older partitions never
 * match, so their archives stay out of every replacement decision.
 */
export const PARTITION_PATTERN = new RegExp(
  '^(?:'
  + `go-build-${CACHE_SNAPSHOT_VERSION}-(?:Linux|macOS|Windows)-(?:X64|ARM64)-[\\w.-]+-go\\d+\\.\\d+\\.\\d+`
  + `|docker-go-build-${CACHE_SNAPSHOT_VERSION}-(?:amd64|arm64)-go\\d+\\.\\d+\\.\\d+`
  + ')-$',
)

export const WEEK_PATTERN_SOURCE = WEEK_PATTERN
