import { readdirSync, readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'bun:test'

const root = resolve(import.meta.dirname, '..')

function yaml(relPath) {
  return Bun.YAML.parse(readFileSync(join(root, relPath), 'utf8'))
}

function raw(relPath) {
  return readFileSync(join(root, relPath), 'utf8')
}

const workflowFiles = readdirSync(join(root, '.github/workflows'))
  .filter(name => name.endsWith('.yaml'))
  .map(name => `.github/workflows/${name}`)

const actionFiles = readdirSync(join(root, '.github/actions'), { withFileTypes: true })
  .filter(entry => entry.isDirectory())
  .map(entry => `.github/actions/${entry.name}/action.yaml`)

function jobSteps(doc) {
  const out = []
  for (const [jobName, job] of Object.entries(doc.jobs ?? {})) {
    for (const step of job.steps ?? []) out.push({ jobName, step })
  }
  return out
}

/** Every step of a workflow or of a composite action (`runs.steps`). */
function allSteps(doc) {
  if (doc.jobs)
    return jobSteps(doc)
  return (doc.runs?.steps ?? []).map(step => ({ jobName: '', step }))
}

/** The steps of a composite action. */
function actionSteps(relPath) {
  return yaml(relPath).runs?.steps ?? []
}

/** The lines of one Dockerfile stage, keyed by its `AS <name>`. */
function dockerStage(name) {
  const lines = raw('docker/Dockerfile').split('\n')
  const starts = []
  for (const [index, line] of lines.entries()) {
    const match = line.match(/^FROM\s+\S+\s+AS\s+(\S+)\s*$/)
    if (match)
      starts.push({ name: match[1], index })
  }
  const stage = starts.find(entry => entry.name === name)
  expect(stage).toBeDefined()
  const next = starts.find(entry => entry.index > stage.index)
  return lines.slice(stage.index + 1, next ? next.index : lines.length)
}

function argLine(lines, arg) {
  return lines.findIndex(line => new RegExp(`^ARG\\s+${arg}(\\s|=|$)`).test(line))
}

function globToRegExp(glob) {
  const body = glob
    .split('*')
    .map(part => part.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'))
    .join('[^/]*')
  return new RegExp(`^${body}$`)
}

describe('GitHub Actions caches', () => {
  it('stores the spinner cache in the directory that Task clones into', () => {
    const step = actionSteps('.github/actions/setup-build/action.yaml')
      .find(entry => entry.name === 'Cache awesome-claude-spinners')
    const path = String(step?.with?.path ?? '')
    const taskSuffix = String(yaml('Taskfile.yaml').vars.SPINNERS_CACHE_DIR.sh).match(/\/\.cache\/[\w-]+/)?.[0]
    expect(taskSuffix).toBe('/.cache/awesome-claude-spinners')
    expect(path.endsWith(taskSuffix)).toBe(true)
  })

  it('builds desktop artifacts on the images that CI tests', () => {
    const ci = yaml('.github/workflows/ci.yaml')
    const desktop = yaml('.github/workflows/desktop-artifacts.yaml')
    for (const jobName of ['linux', 'macos', 'windows']) {
      expect(desktop.jobs[jobName]['runs-on']).toBe(ci.jobs[jobName]['runs-on'])
    }
  })

  it('saves compiled caches only from the CI platform jobs', () => {
    const savers = []
    for (const file of workflowFiles) {
      for (const { jobName, step } of jobSteps(yaml(file))) {
        if (step.uses !== './.github/actions/setup-build')
          continue
        expect(step.with?.['rust-save-if']).toBeUndefined()
        if ('save-build-caches' in (step.with ?? {})) {
          savers.push({ job: `${file}:${jobName}`, value: step.with['save-build-caches'] })
        }
      }
    }
    expect(savers.map(saver => saver.job).sort()).toEqual([
      '.github/workflows/ci.yaml:linux',
      '.github/workflows/ci.yaml:macos',
      '.github/workflows/ci.yaml:windows',
    ])
    const mainOnlySave = /^\$\{\{ github\.ref == 'refs\/heads\/main' \}\}$/
    for (const saver of savers) {
      expect(saver.value).toMatch(mainOnlySave)
    }
    for (const file of [...workflowFiles, ...actionFiles]) {
      expect(raw(file)).not.toContain('rust-save-if')
    }
  })

  it('keeps per-commit build args out of the toolchain layers', () => {
    const copyIndex = dockerStage('builder').findIndex(line => /^COPY\s+\.\s+\.\s*$/.test(line))
    expect(copyIndex).toBeGreaterThanOrEqual(0)
    for (const arg of ['VERSION', 'COMMIT_HASH', 'COMMIT_TIME']) {
      expect(argLine(dockerStage('builder'), arg)).toBeGreaterThan(copyIndex)
      expect(argLine(dockerStage('tools'), arg)).toBe(-1)
    }
  })

  it('fills only cache mounts that the Dockerfile declares', () => {
    const targets = new Set()
    for (const step of actionSteps('.github/actions/build-docker-image/action.yaml')) {
      if (!String(step.uses ?? '').startsWith('reproducible-containers/buildkit-cache-dance'))
        continue
      for (const target of Object.values(JSON.parse(step.with['cache-map']))) targets.add(target)
    }
    expect(targets.size).toBeGreaterThan(0)
    const mounts = new Set()
    for (const line of dockerStage('builder')) {
      for (const match of line.matchAll(/--mount=type=cache,target=([^,\s]+)/g)) mounts.add(match[1])
    }
    for (const target of targets) expect(mounts.has(target)).toBe(true)
  })

  it('exports only the final-image layers for the Docker image cache', () => {
    const steps = actionSteps('.github/actions/build-docker-image/action.yaml')
    const byName = new Map(steps.map(step => [step.name, step]))
    const tools = byName.get('Build the tools stage')
    expect(tools?.with?.target).toBe('tools')
    expect(String(tools?.with?.['cache-to'] ?? '')).toContain('mode=max')
    const image = byName.get('Build the image')
    expect(String(image?.with?.['cache-to'] ?? '')).toContain('mode=min')
    for (const step of steps) {
      if (String(step.with?.['cache-to'] ?? '').includes('mode=max')) {
        expect(step.name).toBe('Build the tools stage')
      }
    }
  })

  it('keeps cross-OS archives inside the workspace', () => {
    for (const file of [...workflowFiles, ...actionFiles]) {
      for (const { step } of allSteps(yaml(file))) {
        if (String(step.with?.enableCrossOsArchive ?? '') !== 'true')
          continue
        for (const path of String(step.with.path).split('\n').map(line => line.trim()).filter(Boolean)) {
          expect(path.startsWith('~')).toBe(false)
          expect(path.startsWith('/')).toBe(false)
          expect(path.includes('${{')).toBe(false)
        }
      }
    }
  })

  it('saves only package files in the APT cache', () => {
    for (const file of [...workflowFiles, ...actionFiles]) {
      for (const { step } of allSteps(yaml(file))) {
        if (!String(step.uses ?? '').startsWith('actions/cache'))
          continue
        if (!String(step.with?.path ?? '').toLowerCase().includes('apt'))
          continue
        for (const path of String(step.with.path).split('\n').map(line => line.trim()).filter(Boolean)) {
          expect(path.endsWith('*.deb')).toBe(true)
        }
      }
    }
  })

  it('leaves Go module caching out of setup-go', () => {
    const step = actionSteps('.github/actions/setup-build/action.yaml')
      .find(entry => entry.name === 'Setup Go')
    expect(String(step?.with?.cache ?? '')).toBe('false')
  })

  it('removes the ambient Rust toolchains before rust-cache computes its key', () => {
    const steps = actionSteps('.github/actions/setup-build/action.yaml')
    const names = steps.map(entry => entry.name)
    const removeIndex = names.indexOf('Remove ambient Rust toolchains')
    const cacheIndex = steps.findIndex(entry =>
      String(entry.uses ?? '').startsWith('Swatinem/rust-cache'))
    expect(removeIndex).toBeGreaterThanOrEqual(0)
    expect(cacheIndex).toBeGreaterThan(removeIndex)
    const run = String(steps[removeIndex].run ?? '')
    expect(run).toContain('rustup default "$RUST_VERSION"')
    expect(run).toContain('rustup toolchain uninstall')
  })

  it('saves each dependency archive right after its full download', () => {
    const steps = actionSteps('.github/actions/dependency-caches/action.yaml')
    const commands = ['go mod download', 'bun install --frozen-lockfile --ignore-scripts', 'cargo fetch --locked']
    const downloaded = new Set()
    for (const [index, step] of steps.entries()) {
      if (!String(step.uses ?? '').startsWith('actions/cache/save'))
        continue
      const download = steps[index - 1]
      const command = commands.find(candidate => String(download?.run ?? '').includes(candidate))
      expect(command).toBeDefined()
      downloaded.add(command)
      expect(step.if).toBe(download.if)
      const key = String(step.with?.key ?? '')
      const match = key.match(/^\$\{\{\s*steps\.(\w+)\.outputs\.cache-primary-key\s*\}\}$/)
      expect(match).not.toBeNull()
      const restore = steps.find(entry => entry.id === match[1])
      expect(String(restore?.uses ?? '').startsWith('actions/cache/restore')).toBe(true)
    }
    for (const command of commands) expect(downloaded.has(command)).toBe(true)
    for (const step of steps) expect(step.with?.['restore-keys']).toBeUndefined()
  })

  it('runs NOTICE again when an action that it uses changes', () => {
    const paths = yaml('.github/workflows/notice.yaml').on.push.paths
    const needed = new Set(['.github/actions/setup-build/action.yaml'])
    const queue = [...needed]
    while (queue.length > 0) {
      for (const step of actionSteps(queue.pop())) {
        const uses = String(step.uses ?? '')
        if (!uses.startsWith('./.github/actions/'))
          continue
        const file = `${uses.slice(2)}/action.yaml`
        if (needed.has(file))
          continue
        needed.add(file)
        queue.push(file)
      }
    }
    for (const file of needed) expect(paths).toContain(file)
  })

  it('pins every remote action to a full commit SHA with a version comment', () => {
    const pin = /^\s*(?:-\s*)?uses:\s*[\w.-]+\/[\w./-]+@[0-9a-f]{40}\s+#\s*\S+\s*$/
    const violations = []
    for (const file of [...workflowFiles, ...actionFiles]) {
      for (const line of raw(file).split('\n')) {
        const match = line.match(/^\s*(?:-\s*)?uses:\s*(\S+)/)
        if (!match)
          continue
        if (line.trimStart().startsWith('#'))
          continue
        if (match[1].startsWith('./') || match[1].startsWith('/'))
          continue
        if (!pin.test(line))
          violations.push(`${file}: ${line.trim()}`)
      }
    }
    expect(violations).toEqual([])
  })

  it('covers each workflow and composite action with Dependabot', () => {
    const entry = yaml('.github/dependabot.yml').updates.find(candidate => candidate['package-ecosystem'] === 'github-actions')
    expect(entry).toBeDefined()
    const directories = Array.isArray(entry.directories) ? entry.directories : [entry.directory]
    expect(directories).toContain('/')
    const names = readdirSync(join(root, '.github/actions'), { withFileTypes: true })
      .filter(candidate => candidate.isDirectory())
      .map(candidate => candidate.name)
    for (const name of names) {
      const target = `/.github/actions/${name}`
      expect(directories.some(glob => globToRegExp(String(glob)).test(target))).toBe(true)
    }
  })
})
