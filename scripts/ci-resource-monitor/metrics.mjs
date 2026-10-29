export function formatGigabytes(bytes) {
  return Number.isFinite(bytes) && bytes >= 0
    ? `${(bytes / 2 ** 30).toFixed(1)}G`
    : 'unknown'
}

export function formatMegabytes(bytes) {
  return Number.isFinite(bytes) && bytes >= 0
    ? `${Math.round(bytes / 2 ** 20)}M`
    : 'unknown'
}

/** The CPU use between two readings of the idle and total CPU time. */
export function cpuPercent(previous, current) {
  if (!previous)
    return 'n/a'
  const total = current.total - previous.total
  if (total <= 0)
    return 'n/a'
  return `${Math.round(100 * (1 - (current.idle - previous.idle) / total))}%`
}
