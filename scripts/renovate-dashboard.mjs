import { advisoryRange, isInAdvisoryRange, patchedIdentifier } from './renovate-advisory-range.mjs'
import { ECOSYSTEMS } from './renovate-ecosystems.mjs'
import { registerRootHelper, renovateModule } from './renovate-engine.mjs'
import { describeAge, releaseAgeMs } from './renovate-release-age.mjs'

// The Dependency Dashboard lists, under Skipped Dependencies, each update and security
// alert that Renovate did not propose, with the reason. Renovate loads this module from
// its configuration under Node, so the module graph uses no Bun API.

/** The managers that can update a dependency of each alerted ecosystem, by its GitHub name. */
const ALERT_MANAGERS = Object.freeze({
  ...Object.fromEntries(Object.values(ECOSYSTEMS).map(({ advisory, kind }) => [
    advisory,
    Object.freeze([kind]),
  ])),
  actions: Object.freeze(['github-actions']),
})

/** The block title of each skip reason. Any other reason shows its own code. */
const SKIP_REASON_TITLES = Object.freeze({
  'disabled': 'Disabled by configuration',
  'ignored': 'Ignored by configuration',
  'contains-variable': 'Version comes from a variable',
})

const TRANSITIVE = 'Transitive dependency. Renovate updates only dependencies that a manifest declares.'

// Renovate reads its own checkbox markers from the dashboard body. An identifier must never
// spell one, so a word joiner splits each marker inside the rendered text.
function inert(value) {
  return String(value)
    .replace(/[\r\n]+/g, ' ')
    .replaceAll('-branch=', '-branch⁠=')
    .replaceAll('config-migration-pr-info', 'config-migration⁠-pr-info')
}

/** Render an identifier as an inline code span that no backtick inside it can end. */
export function code(value) {
  const text = inert(value)
  const fence = '`'.repeat(Math.max(0, ...[
    ...text.matchAll(/`+/g),
  ].map(match => match[0].length)) + 1)
  const pad = text.startsWith('`') || text.endsWith('`')
    ? ' '
    : ''
  return `${fence}${pad}${text}${pad}${fence}`
}

const escapeHtml = value => String(value).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;')

function packageFilesOf(dataByBranch) {
  const files = []
  for (const managers of dataByBranch.values()) {
    for (const [manager, packageFiles] of Object.entries(managers ?? {})) {
      for (const packageFile of packageFiles ?? []) {
        files.push({
          manager,
          packageFile: packageFile.packageFile,
          deps: (packageFile.deps ?? []).filter(Boolean),
        })
      }
    }
  }
  return files
}

// Collect entries by key, so that a dependency in several files appears once.
function collect(entries) {
  const merged = new Map()
  for (const { key, files, ...entry } of entries) {
    const known = merged.get(key) ?? {
      ...entry,
      files: new Set(),
    }
    for (const file of files)
      known.files.add(file)
    merged.set(key, known)
  }
  return [
    ...merged.values(),
  ].map(entry => ({
    ...entry,
    files: [...entry.files].sort(),
  }))
}

// An update fixes an alert when its new version leaves the vulnerable range. An update
// that states no comparable version, such as a digest update, counts as a fix, because
// the dashboard cannot show that it leaves the alert open.
function fixesAlert(update, range) {
  if (typeof update.newVersion !== 'string' || advisoryRange(range) === null)
    return true
  return !isInAdvisoryRange(update.newVersion, range)
}

const fileList = files => files.map(code).join(', ')
// en-CA formats a date as YYYY-MM-DD.
function dateOf(time, timeZone) {
  return new Intl.DateTimeFormat('en-CA', {
    timeZone,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  }).format(time)
}

function alertBlock(files, alerts) {
  const entries = []
  for (const alert of alerts) {
    if (alert?.state !== 'open')
      continue
    const vulnerability = alert.security_vulnerability ?? {}
    const name = vulnerability.package?.name
    const ecosystem = vulnerability.package?.ecosystem
    if (typeof name !== 'string')
      continue
    const patched = patchedIdentifier(vulnerability)
    const managers = ALERT_MANAGERS[ecosystem]
    const deps = managers
      ? files.filter(file => managers.includes(file.manager)).flatMap(file => file.deps.filter(dep => dep.depName === name || dep.packageName === name))
      : []
    const updates = deps.flatMap(dep => (dep.updates ?? []).filter(update => update.updateType !== 'lockFileMaintenance'))
    const range = vulnerability.vulnerable_version_range
    const fixes = updates.filter(update => fixesAlert(update, range))
    let reason
    if (patched === null) {
      reason = 'No patched version exists.'
    }
    else if (!managers) {
      reason = `Renovate manages no dependency of the ${code(ecosystem ?? 'unknown')} ecosystem.`
    }
    else if (deps.length === 0) {
      reason = TRANSITIVE
    }
    else if (deps.every(dep => dep.skipReason)) {
      reason = `Renovate skips it: ${[
        ...new Set(deps.map(dep => SKIP_REASON_TITLES[dep.skipReason] ?? code(dep.skipReason))),
      ].join(', ')}.`
    }
    else if (updates.length === 0) {
      reason = 'A patched version exists, but Renovate found no update.'
    }
    else if (fixes.length === 0) {
      reason = 'Every update that Renovate found stays in the vulnerable range.'
    }
    else if (fixes.every(update => update.pendingChecks)) {
      reason = 'Every update that fixes it waits for the minimum release age.'
    }
    else {
      continue
    }
    const advisory = alert.security_advisory?.ghsa_id
    const link = typeof advisory === 'string' && /^GHSA(?:-[2-9cfghjmpqrvwx]{4}){3}$/.test(advisory)
      ? `[${advisory}](https://github.com/advisories/${advisory})`
      : code(advisory ?? 'an advisory')
    entries.push({
      key: JSON.stringify([advisory, ecosystem, name]),
      files: [alert.dependency?.manifest_path ?? 'an unknown manifest'],
      render: entry => `${code(name)} in ${fileList(entry.files)}: ${link} affects ${code(range ?? 'an unknown range')}${patched === null
        ? ''
        : `, fixed in ${code(patched)}`}. ${reason}`,
    })
  }
  return {
    title: 'Security alerts without a proposed fix',
    entries: collect(entries),
  }
}

function waitingBlock(files, { ageMs, timeZone }) {
  const entries = []
  for (const file of files) {
    for (const dep of file.deps) {
      for (const update of dep.updates ?? []) {
        if (update.updateType === 'lockFileMaintenance')
          continue
        const subject = update.newVersion
          ? code(update.newVersion)
          : `${update.updateType ?? 'an'} update`
        if (update.pendingChecks) {
          const published = Date.parse(update.releaseTimestamp)
          entries.push({
            key: JSON.stringify(['pending', dep.depName, update.newVersion ?? update.newDigest ?? update.updateType]),
            files: [file.packageFile],
            render: entry => `${code(dep.depName)} (${fileList(entry.files)}): ${subject} ${Number.isFinite(published)
              ? `waits until ${dateOf(published + ageMs, timeZone)}`
              : 'has no release timestamp'}`,
          })
        }
        else if (update.pendingVersions?.length) {
          const waiting = update.pendingVersions.slice(0, 3).map(code).join(', ')
          entries.push({
            key: JSON.stringify([
              'versions',
              dep.depName,
              update.newVersion,
              update.pendingVersions,
            ]),
            files: [file.packageFile],
            render: entry => `${code(dep.depName)} (${fileList(entry.files)}): proposes ${subject}; ${waiting}${update.pendingVersions.length > 3
              ? ', …'
              : ''} wait`,
          })
        }
      }
    }
  }
  return {
    title: `Waiting for the minimum release age (${escapeHtml(describeAge(ageMs))}, dates in ${escapeHtml(timeZone)})`,
    entries: collect(entries),
  }
}

function skipBlocks(files) {
  const byReason = new Map()
  for (const file of files) {
    for (const dep of file.deps) {
      if (!dep.skipReason)
        continue
      const entries = byReason.get(dep.skipReason) ?? []
      entries.push({
        key: JSON.stringify([dep.depName ?? dep.packageName ?? null]),
        files: [file.packageFile],
        render: entry => `${code(dep.depName ?? dep.packageName ?? 'unnamed')} (${fileList(entry.files)})`,
      })
      byReason.set(dep.skipReason, entries)
    }
  }
  return [...byReason].map(([reason, entries]) => ({
    title: SKIP_REASON_TITLES[reason] ?? `<code>${escapeHtml(reason)}</code>`,
    entries: collect(entries),
  })).sort((a, b) => a.title.localeCompare(b.title))
}

function renderBlocks(blocks, limit) {
  const body = blocks.map(({ title, entries }) => {
    const shown = entries.slice(0, limit).map(entry => `- ${entry.render(entry)}`)
    if (entries.length > shown.length)
      shown.push(`- …and ${entries.length - shown.length} more`)
    return `<details><summary>${title} (${entries.length})</summary>\n\n${shown.join('\n')}\n\n</details>`
  })
  return `## Skipped Dependencies\n\n${body.length > 0
    ? body.join('\n\n')
    : 'None.'}\n`
}

/**
 * Render the Skipped Dependencies section from Renovate's package files by base branch.
 * A date is a calendar day in `timeZone`, the zone of Renovate's schedule.
 * The lists shorten, each to the same number of items, until the section fits `maxLength`.
 */
export function renderSkippedDependencies(dataByBranch, { alerts = [], ageMs, timeZone = 'UTC', maxLength = 20000 }) {
  const files = packageFilesOf(dataByBranch)
  const blocks = [
    alertBlock(files, alerts),
    waitingBlock(files, { ageMs, timeZone }),
    ...skipBlocks(files),
  ].filter(block => block.entries.length > 0)
  const longest = Math.max(0, ...blocks.map(block => block.entries.length))
  const complete = renderBlocks(blocks, longest)
  if (complete.length <= maxLength)
    return complete
  // Find the largest item count that fits.
  let low = 0
  let high = longest
  while (low < high) {
    const middle = Math.ceil((low + high) / 2)
    if (renderBlocks(blocks, middle).length <= maxLength)
      low = middle
    else
      high = middle - 1
  }
  const rendered = renderBlocks(blocks, low)
  if (rendered.length > maxLength)
    throw new Error(`The Skipped Dependencies section cannot fit in ${maxLength} characters.`)
  return rendered
}

/**
 * Register the dashboard footer helper on Renovate's Handlebars instance. The helper
 * renders only from the dashboard's own repository context, never from an upgrade.
 */
export async function configureSkippedDependencies({ policy, alerts }) {
  const { PackageFiles } = await renovateModule('workers/repository/package-files.js')
  const ageMs = releaseAgeMs(policy)
  // Without a policy time zone, Renovate schedules in the runner's zone, which is UTC on
  // GitHub-hosted runners.
  const timeZone = policy.timezone ?? 'UTC'
  // An unknown zone fails here, before the helper exists, and not in the middle of a run.
  dateOf(0, timeZone)
  const dispose = registerRootHelper('leapmuxSkippedDependencies', {
    // The dashboard renders from the repository context, which holds no upgrade field.
    accepts: root => [
      'manager',
      'packageFile',
      'upgrades',
      'depName',
    ].every(field => root?.[field] === undefined),
    rejection: 'Skipped dependencies can only be rendered in the Dependency Dashboard footer.',
  }, () => {
    if (PackageFiles.data.size === 0)
      throw new Error('Skipped dependencies require the package files that Renovate extracted.')
    return renderSkippedDependencies(PackageFiles.data, { alerts, ageMs, timeZone })
  })
  const footer = '{{{leapmuxSkippedDependencies}}}'
  return {
    // A footer that the policy sets stays, above the section.
    dependencyDashboardFooter: typeof policy.dependencyDashboardFooter === 'string' && policy.dependencyDashboardFooter.trim() !== ''
      ? `${policy.dependencyDashboardFooter}\n\n${footer}`
      : footer,
    dispose,
  }
}
