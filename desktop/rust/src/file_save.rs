//! Streaming file-save subsystem.
//!
//! Saves are streamed through a `file_save_open[_dialog] → file_save_write* →
//! file_save_commit | file_save_abort` chain. The Rust side keeps the destination
//! `File` open in a registry between calls, so the JS caller pipes each 1 MiB
//! worker chunk straight through `file_save_write` without materializing the
//! whole file (bounding peak transient memory to a few MiB even for
//! multi-hundred-MB downloads). Writes go to a sibling temp file
//! (`<final>.leapmux.tmp`); commit atomic-renames it onto the final path, abort
//! deletes it -- so the final name never appears on disk until the save
//! completes, and (for Save as...) the user's existing file is preserved on
//! failure.
//!
//! Extracted from `main.rs`.

use std::collections::{HashMap, HashSet};
use std::ffi::OsStr;
use std::io::{self, Write};
use std::path::{Path, PathBuf};
use std::sync::{
    atomic::{AtomicU64, Ordering},
    Arc, Mutex,
};
use std::time::{Duration, Instant};

use tauri::State;
use tokio::sync::oneshot;

use serde::Serialize;

use crate::{decode_b64, recover};

// Saves are streamed through a `file_save_open[_dialog] → file_save_write* →
// file_save_commit | file_save_abort` chain. The Rust side keeps the
// destination `File` open in a registry between calls, so the JS caller
// can pipe each 1 MiB worker chunk straight through `file_save_write`
// without ever materializing the whole file. This bounds the peak
// transient memory (per chunk × ~3 copies across the IPC boundary) to
// a few MiB even for multi-hundred-MB downloads.
//
// Writes go to a sibling temp file (`<final>.leapmux.tmp`); on commit we
// atomic-rename it onto the final path, and on abort we delete it.
// Consequence: the final name never appears on disk until the save is
// complete, and (for Save as...) the user's existing file at the chosen
// path is preserved if the save fails. The Downloads variant iterates
// candidate names ("foo.ext",
// "foo (1).ext", ...) skipping any whose final path already exists and
// claiming each candidate's `<name>.leapmux.tmp` with `create_new` — that
// single open both reserves the iteration spot against concurrent
// LeapMux saves of the same basename and provides the file the bytes
// stream into.

fn read_header_str<'a>(
    request: &'a tauri::ipc::Request<'_>,
    name: &str,
) -> Result<&'a str, String> {
    request
        .headers()
        .get(name)
        .ok_or_else(|| format!("missing {name} header"))?
        .to_str()
        .map_err(|err| format!("invalid {name} header: {err}"))
}

fn read_b64_header(request: &tauri::ipc::Request<'_>, name: &str) -> Result<String, String> {
    let bytes = decode_b64(read_header_str(request, name)?)?;
    String::from_utf8(bytes).map_err(|err| format!("invalid {name} utf-8: {err}"))
}

/// Parse the decimal `handle-id` header shared by `file_save_write`,
/// `file_save_commit`, and `file_save_abort`.
fn read_handle_id(request: &tauri::ipc::Request<'_>) -> Result<u64, String> {
    read_header_str(request, "handle-id")?
        .parse()
        .map_err(|err| format!("invalid handle-id: {err}"))
}

/// Run a blocking closure on the dedicated blocking-thread pool and
/// return its result, surfacing a join failure as an error string. Used
/// for save-stream operations that touch the disk and shouldn't tie up
/// the async executor thread servicing other Tauri commands.
async fn run_blocking<F, T>(f: F) -> Result<T, String>
where
    F: FnOnce() -> Result<T, String> + Send + 'static,
    T: Send + 'static,
{
    tokio::task::spawn_blocking(f)
        .await
        .map_err(|err| format!("spawn_blocking join: {err}"))?
}

/// Cap on collision-dedup attempts. With "foo (N).ext" picking from
/// `1..MAX`, this bounds the directory scan and the suffix the user
/// sees ("foo (1023).ext" is well past the point where a different
/// filename is more useful than another increment).
pub(crate) const MAX_SAVE_COLLISION_ATTEMPTS: u32 = 1024;

/// Suffix appended to the final path to form the streaming partial.
/// Shared by the producer (`tmp_path_for`), the matcher (`is_partial_name`,
/// used by `sweep_orphan_tmps`), and the defuser (`defuse_final_path`) so
/// the three cannot drift.
///
/// Two invariants load-bearing for #285:
/// - Distinctive (`.leapmux.tmp`, not a bare `.tmp`): distinctive enough
///   that the startup sweep never matches a generic `*.tmp` some other
///   tool left in Downloads. This is a naming convention, not a hard
///   guarantee against a foreign file that deliberately reuses the
///   suffix; what makes the sweep safe for *our* files is that
///   `defuse_final_path` keeps every LeapMux final clear of the suffix,
///   so a match is a LeapMux partial by construction.
/// - Deterministic (no PID/randomness): `create_new` on this fixed
///   sibling name is what reserves a collision slot against concurrent
///   same-name saves.
pub(crate) const SAVE_TMP_SUFFIX: &str = ".leapmux.tmp";

/// Appended to a chosen final whose own name would match `is_partial_name`,
/// so a committed final can never be mistaken for a partial by the sweep.
/// The single source of truth for the defuse marker, shared by both defuse
/// sites (`defuse_final_path` and the inline defuse in `open_unique_tmp`) so
/// the two cannot drift — the same role `SAVE_TMP_SUFFIX` plays for the
/// partial suffix. Must not itself end in `SAVE_TMP_SUFFIX`.
pub(crate) const SAVE_DEFUSE_SUFFIX: &str = ".download";

/// How often the idle-handle GC scans the registry for handles whose
/// JS pump appears to have died. 60s keeps the scan cost negligible
/// while still bounding orphan-disk-junk lifetime to roughly
/// `SAVE_HANDLE_GC_INTERVAL + SAVE_HANDLE_IDLE_TIMEOUT`.
pub(crate) const SAVE_HANDLE_GC_INTERVAL: Duration = Duration::from_secs(60);

/// How long a handle can sit without a `write_chunk` (or `close`)
/// before the GC discards it. An active save touches `last_write_at`
/// per chunk, so the gap can only widen if the JS pump is wedged or
/// the renderer process died. 5 min is well above any realistic
/// per-chunk latency (1 MiB chunks rarely take more than seconds) but
/// short enough that an orphan partial is gone before the user notices.
pub(crate) const SAVE_HANDLE_IDLE_TIMEOUT: Duration = Duration::from_secs(300);

/// Registry entry for a save in progress: the open file plus the
/// paths needed to finalize or discard. Distinct from
/// `SaveStreamHandle`, which is the id+path token JS holds; this
/// struct is Rust-only and never crosses the IPC boundary.
pub(crate) struct OpenSaveStream {
    /// `Arc<Mutex<File>>` rather than `Mutex<File>` so `write_chunk` can
    /// short-lock the registry to clone the Arc, drop the registry
    /// lock, and then take the per-file lock — writes to different
    /// streams run in parallel instead of serializing on a registry-
    /// wide mutex.
    pub(crate) file: Arc<Mutex<std::fs::File>>,
    /// Sibling `<final>.leapmux.tmp` path that bytes stream into.
    tmp_path: PathBuf,
    /// Final destination — the partial is atomic-renamed onto this on success.
    final_path: PathBuf,
    /// Updated on insert and on every `write_chunk`. The idle-handle
    /// GC compares this against `SAVE_HANDLE_IDLE_TIMEOUT` to detect
    /// JS pumps that died without calling `file_save_commit` or
    /// `file_save_abort`. Lives under the registry `Mutex<HashMap>`
    /// lock so it shares the existing critical section instead of
    /// needing its own atomic.
    last_write_at: Instant,
}

/// Open destination files keyed by a monotonic u64 id. The JS caller
/// receives the id from `file_save_open[_dialog]` and submits it back
/// with each `file_save_write` and the final `file_save_commit` or
/// `file_save_abort`.
pub(crate) struct SaveStreamRegistry {
    /// Starts at 1 so a freshly constructed registry never hands out 0 —
    /// keeps "0 == sentinel" assumptions on the JS side safe.
    next_id: AtomicU64,
    // Recovers from poisoning via the `recover` helper; the per-handle
    // `Arc<Mutex<File>>` in `write_chunk` is the exception and fails closed.
    // See the PendingMap comment above and
    // https://github.com/leapmux/leapmux/issues/277.
    pub(crate) handles: Mutex<HashMap<u64, OpenSaveStream>>,
}

impl SaveStreamRegistry {
    pub(crate) fn new() -> Self {
        Self {
            next_id: AtomicU64::new(1),
            handles: Mutex::new(HashMap::new()),
        }
    }

    /// Insert a freshly-opened file into the registry and return the
    /// JS-facing handle (id + the final path as a UTF-8 string).
    /// Both `file_save_open` and `file_save_open_dialog` end with this,
    /// so it lives here to keep the id/path packaging in one spot.
    pub(crate) fn insert(
        &self,
        file: std::fs::File,
        tmp_path: PathBuf,
        final_path: PathBuf,
    ) -> SaveStreamHandle {
        let id = self.next_id.fetch_add(1, Ordering::Relaxed);
        let path = final_path.to_string_lossy().into_owned();
        recover(&self.handles).insert(
            id,
            OpenSaveStream {
                file: Arc::new(Mutex::new(file)),
                tmp_path,
                final_path,
                last_write_at: Instant::now(),
            },
        );
        SaveStreamHandle { id, path }
    }

    fn take(&self, id: u64) -> Option<OpenSaveStream> {
        recover(&self.handles).remove(&id)
    }

    /// Finalize the save: take the handle, ensure no write is still in flight,
    /// then atomic-rename the partial onto the final path. On any failure the
    /// partial is discarded.
    ///
    /// The in-flight-write check is load-bearing. `write_chunk` clones the
    /// per-handle `Arc<Mutex<File>>` and writes with the registry lock
    /// released, so a duplicated/overlapping `file_save_write` (a buggy or
    /// retrying JS pump that does not await the previous write) could still be
    /// holding a clone when commit runs. Committing anyway would, on Windows,
    /// fail the rename with an opaque "used by another process" error, and on
    /// Unix SUCCEED while the in-flight write appends to the just-renamed file
    /// -- silent corruption. So after `take` (which removes the registry's own
    /// clone, and after which `write_chunk` can create no new one -- its
    /// `get_mut` returns None), `Arc::try_unwrap` is the reliable
    /// sole-ownership test: if it fails, a write clone is live, and commit
    /// fails loudly and discards the partial rather than racing it.
    pub(crate) fn commit(&self, id: u64) -> Result<(), String> {
        let Some(stream) = self.take(id) else {
            return Err(format!("unknown save handle {id}"));
        };
        let OpenSaveStream {
            file,
            tmp_path,
            final_path,
            last_write_at: _,
        } = stream;
        // Sole-ownership check: see the method doc. Arc::try_unwrap returns the
        // inner Mutex only when this is the last reference.
        let file = match Arc::try_unwrap(file) {
            Ok(file) => file,
            Err(_) => {
                discard_partials(&tmp_path);
                return Err(format!(
                    "save handle {id} has a write still in progress; commit refused"
                ));
            }
        };
        // No `sync_all` before the rename -- intentional. A `sync_all` on a
        // multi-hundred-MB save blocks for seconds while the OS flushes the
        // page cache, and neither flow has a contract that needs it: the
        // Downloads variant only ever writes to a path that was empty when we
        // picked the name (so a power-loss window between rename and OS flush
        // just loses the new file, it can't corrupt anything else), and the
        // Save-as variant's overwrite is already non-atomic at the
        // user-content level (the user picked a path knowing it would be
        // replaced, and can re-save on crash). Don't add a sync here without
        // matching the latency cost to a concrete guarantee we actually need to
        // make.
        //
        // Drop the File before the rename: Windows refuses `rename`/
        // `remove_file` while the handle is open. try_unwrap above proved this
        // is the sole owner, so this drop releases the underlying File.
        drop(file);
        // `std::fs::rename` replaces the destination on both Unix and Windows.
        // For save-as that overwrites the user's prior content (the path they
        // chose). For the Downloads flow the final path was empty when we
        // picked the name (`open_unique_tmp` skips candidates whose final
        // already exists); a file appearing there mid-stream is a TOCTOU race
        // we accept.
        let result =
            std::fs::rename(&tmp_path, &final_path).map_err(|err| format!("rename: {err}"));
        if result.is_err() {
            discard_partials(&tmp_path);
        }
        result
    }

    pub(crate) fn write_chunk(&self, id: u64, bytes: &[u8]) -> Result<(), String> {
        // Lock the registry only long enough to refresh the idle
        // timestamp and clone the per-handle Arc, then drop the
        // registry lock before acquiring the file lock. Concurrent
        // writes targeting different handles can then proceed in
        // parallel.
        let file = {
            let mut guard = recover(&self.handles);
            let handle = guard
                .get_mut(&id)
                .ok_or_else(|| format!("unknown save handle {id}"))?;
            handle.last_write_at = Instant::now();
            handle.file.clone()
        };
        // The per-handle file lock is the one lock in this crate that does NOT
        // recover from poisoning. Poisoning here means the guard was held
        // across a panic (realistically: an allocator OOM or a future panicking
        // op inside the critical section -- `File::write_all` itself returns
        // `Result` and does not panic on I/O errors), leaving the tmp file in
        // an indeterminate state.
        //
        // Recovering silently and continuing to write would corrupt the save.
        // But surfacing the error alone is NOT enough either: a panic unwinds
        // and drops this frame's `Arc` clone, so by the time the caller acts
        // the registry's clone is again the sole owner. `commit` would then
        // `take` the handle, `Arc::try_unwrap` would SUCCEED (`try_unwrap`
        // catches a *live* write clone, not a *panicked* one whose clone has
        // unwound away), and `rename` would atomically promote the corrupted
        // -- or, for save-as on a first write, 0-byte -- partial onto the
        // user's chosen final path, reported as success. In the save-as flow
        // (`open_tmp_for_write` opens with `truncate`), that silently destroys
        // the user's original file.
        //
        // So discard the handle and its partial on this path: `take` removes
        // the registry entry (and `commit`/`write_chunk` now find "unknown
        // save handle {id}" -- commit cannot rename what is gone), and
        // `discard_stream` drops the poisoned `Arc<Mutex<File>>` and removes
        // the tmp in the documented Windows-safe order (File dropped before
        // remove). The caller still gets the error to surface. See
        // https://github.com/leapmux/leapmux/issues/277.
        let mut guard = match file.lock() {
            Ok(g) => g,
            Err(_) => {
                if let Some(stream) = self.take(id) {
                    discard_stream(stream);
                }
                return Err(format!(
                    "save handle {id} write failed: a prior write panicked"
                ));
            }
        };
        guard
            .write_all(bytes)
            .map_err(|err| format!("write: {err}"))
    }

    /// Drop all open handles and remove any partial files. Called from
    /// the app exit path so an interrupted save doesn't leave junk on
    /// disk.
    pub(crate) fn cleanup_all(&self) {
        let drained: Vec<_> = recover(&self.handles).drain().collect();
        for (_, stream) in drained {
            discard_stream(stream);
        }
    }

    /// Discard handles whose `last_write_at` is older than `max_idle`.
    /// Two-phase: snapshot stale ids under a brief lock, then `take`
    /// each individually so the per-discard `remove_file` syscalls
    /// never run under the registry lock. A handle being actively
    /// written to during the scan window is fine — a racing
    /// `write_chunk` refreshes `last_write_at`, and the take in phase
    /// 2 re-checks against `max_idle` and skips it.
    ///
    /// This cleanup is in-memory only: it (and `cleanup_all` on graceful
    /// exit) covers a dead JS pump or a normal quit, but a hard process
    /// death (SIGKILL, crash, power loss) takes this registry down with it
    /// and strands the partial on disk. `sweep_orphan_tmps` reclaims those
    /// left in Downloads at the next startup (a Save-as partial stranded
    /// elsewhere is inert, not swept -- see `open_tmp_for_write`); until
    /// then a stale partial is indistinguishable from a live reservation
    /// and forces a spurious "(N)" suffix (see `open_unique_tmp`). See
    /// https://github.com/leapmux/leapmux/issues/285.
    pub(crate) fn gc_idle(&self, max_idle: Duration) {
        let now = Instant::now();
        let stale_ids: Vec<u64> = {
            let guard = recover(&self.handles);
            guard
                .iter()
                .filter(|(_, h)| now.duration_since(h.last_write_at) >= max_idle)
                .map(|(id, _)| *id)
                .collect()
        };
        for id in stale_ids {
            // Re-check under lock: a `write_chunk` racing between
            // snapshot and take may have refreshed the timestamp. If
            // it has, leave the stream alone.
            let stream = {
                let mut guard = recover(&self.handles);
                match guard.get(&id) {
                    Some(h) if now.duration_since(h.last_write_at) >= max_idle => guard.remove(&id),
                    _ => None,
                }
            };
            if let Some(stream) = stream {
                discard_stream(stream);
            }
        }
    }

    /// Delete orphaned save partials under `dir` left by a prior hard
    /// process death. Safe at the sole (startup) call site because three
    /// legs hold together there:
    ///
    /// 1. Distinctive suffix (`is_partial_name`) — a match is a LeapMux
    ///    save partial by construction: `defuse_final_path` keeps every
    ///    LeapMux *final* clear of `SAVE_TMP_SUFFIX`, and a generic `*.tmp`
    ///    from another tool never matches.
    /// 2. `tauri-plugin-single-instance` — no other LeapMux process can
    ///    be mid-save when this runs at startup.
    /// 3. The registry is empty and not yet `manage`d at the call site, so
    ///    none of our own saves is in flight.
    ///
    /// The live-`tmp_path` cross-check below spares any partial already in
    /// the registry, but it is NOT enough on its own to make a *periodic*
    /// call safe: `live` is snapshotted before `read_dir`, so a save that
    /// starts afterward is on disk yet absent from the snapshot, and the
    /// comparison is byte-exact on uncanonicalized paths. A future periodic
    /// caller must first close that race (snapshot under the same lock that
    /// guards insert, or re-check membership immediately before each
    /// remove) and pass the identical `dirs::download_dir()` value
    /// `file_save_open` uses. See https://github.com/leapmux/leapmux/issues/285.
    pub(crate) fn sweep_orphan_tmps(&self, dir: &Path) {
        let live: HashSet<PathBuf> = {
            let guard = recover(&self.handles);
            guard.values().map(|h| h.tmp_path.clone()).collect()
        };
        let entries = match std::fs::read_dir(dir) {
            Ok(entries) => entries,
            // A missing Downloads dir is benign (fresh machine, or a
            // relocated/unmounted volume) -- return quietly. Any other
            // failure (permissions, transient I/O) leaves orphans behind,
            // so log it rather than no-op invisibly, matching the per-entry
            // branches below.
            Err(err) if err.kind() == io::ErrorKind::NotFound => return,
            Err(err) => {
                crate::shell_log!("sweep read dir {}: {err}", dir.display());
                return;
            }
        };
        for entry in entries {
            // Log and skip a per-entry read error rather than silently
            // dropping it (as `entries.flatten()` would): an orphan behind a
            // transient error self-heals on a later launch, but the failure
            // should not be invisible. Mirrors the remove_file branch below.
            let entry = match entry {
                Ok(entry) => entry,
                Err(err) => {
                    crate::shell_log!("sweep read dir entry in {}: {err}", dir.display());
                    continue;
                }
            };
            if !is_partial_name(&entry.file_name()) {
                continue;
            }
            // Don't follow symlinks: dirs/symlinks named like partials
            // are spared. Log a stat failure rather than skip it silently,
            // as the read-dir and remove_file branches do.
            let ft = match entry.file_type() {
                Ok(ft) => ft,
                Err(err) => {
                    crate::shell_log!("sweep file type {}: {err}", entry.path().display());
                    continue;
                }
            };
            if !ft.is_file() {
                continue;
            }
            let path = entry.path();
            if live.contains(&path) {
                continue;
            }
            if let Err(err) = std::fs::remove_file(&path) {
                crate::shell_log!("sweep orphan save partial {}: {err}", path.display());
            }
        }
    }
}

/// Append `SAVE_TMP_SUFFIX` to `path` while preserving the existing
/// OsString (handles non-UTF-8 paths cleanly).
pub(crate) fn tmp_path_for(final_path: &Path) -> PathBuf {
    let mut name = final_path.as_os_str().to_owned();
    name.push(SAVE_TMP_SUFFIX);
    PathBuf::from(name)
}

/// Whether `name` is a save partial produced by `tmp_path_for`: it ends
/// in `SAVE_TMP_SUFFIX` and is *strictly* longer than the bare suffix. A
/// real final name is never empty, so a partial's name always exceeds the
/// suffix — a file named exactly `.leapmux.tmp` is therefore not ours and
/// is spared. This is the inverse of `tmp_path_for`, kept beside it so the
/// two can't drift, and the exact predicate `sweep_orphan_tmps` deletes
/// on and `defuse_final_path` protects finals against. Byte-wise, so
/// non-UTF-8 names are handled like `tmp_path_for`.
pub(crate) fn is_partial_name(name: &OsStr) -> bool {
    let bytes = name.as_encoded_bytes();
    bytes.len() > SAVE_TMP_SUFFIX.len() && bytes.ends_with(SAVE_TMP_SUFFIX.as_bytes())
}

/// Rewrite a chosen final `path` whose file name would be swept as an
/// orphan partial (`is_partial_name`) by appending `.download`, so a
/// committed final can never be mistaken for — and silently deleted as —
/// a partial by `sweep_orphan_tmps`. The OsString is preserved for
/// non-UTF-8 names. Both save entry points route their final through the
/// same reserved-suffix defuse: the Downloads auto-name flow inside
/// `open_unique_tmp`, and the Save-as dialog via this helper. Without it a
/// server-supplied name like `report.leapmux.tmp` (or a Save-as target
/// typed into Downloads) would commit a real final the next startup sweep
/// removes. See https://github.com/leapmux/leapmux/issues/285.
pub(crate) fn defuse_final_path(path: PathBuf) -> PathBuf {
    if path.file_name().is_some_and(is_partial_name) {
        let mut name = path.into_os_string();
        name.push(SAVE_DEFUSE_SUFFIX);
        PathBuf::from(name)
    } else {
        path
    }
}

/// Resolve a Save-as chosen path to its final write target, applying the
/// reserved-suffix defuse (`defuse_final_path`) and refusing when that defuse
/// redirects the write onto a pre-existing `.download` file the native dialog
/// never confirmed. The dialog's overwrite prompt only covers the name the
/// user picked; when that name ends in `SAVE_TMP_SUFFIX` the bytes actually
/// land on `<name>.download`, so silently replacing an existing file there
/// would destroy data the user was never asked about. The check runs only
/// when the defuse actually rewrote the path (the rare case), so a normal
/// dialog-confirmed overwrite is untouched. A residual TOCTOU window before
/// the commit rename degrades to the prior silent replace -- strictly better
/// than replacing unconditionally. See
/// https://github.com/leapmux/leapmux/issues/285.
pub(crate) fn resolve_save_as_final(chosen: PathBuf) -> Result<PathBuf, String> {
    let final_path = defuse_final_path(chosen.clone());
    if final_path != chosen {
        match final_path.try_exists() {
            Ok(true) => {
                return Err(format!(
                    "cannot save: {} already exists (a name ending in \
                     {SAVE_TMP_SUFFIX} was redirected to avoid the startup sweep)",
                    final_path.display()
                ))
            }
            Ok(false) => {}
            Err(err) => return Err(format!("stat {}: {err}", final_path.display())),
        }
    }
    Ok(final_path)
}

/// Open (or create+truncate) the temp sibling of `final_path` for
/// streaming writes. Used by the Save-as dialog flow
/// (`file_save_open_dialog`); the Downloads flow reserves and opens its
/// partial with `create_new` inside `open_unique_tmp` instead.
///
/// A hard death here strands the partial in the user's chosen directory.
/// `sweep_orphan_tmps` only reclaims Downloads, so a Save-as partial
/// elsewhere lingers until the user deletes it -- but it is inert: the
/// truncating open takes no collision reservation, so unlike a Downloads
/// orphan it forces no "(N)" suffix on later saves (#285).
pub(crate) fn open_tmp_for_write(final_path: &Path) -> Result<(std::fs::File, PathBuf), String> {
    let tmp_path = tmp_path_for(final_path);
    let tmp_file = std::fs::OpenOptions::new()
        .write(true)
        .create(true)
        .truncate(true)
        .open(&tmp_path)
        .map_err(|err| format!("open tmp file: {err}"))?;
    Ok((tmp_file, tmp_path))
}

/// Remove the partial temp file. Used by both `discard_stream` and the
/// failure branches of `file_save_commit`, which have already dropped
/// the `File` themselves. The final path is never ours to remove —
/// nothing was ever created there.
pub(crate) fn discard_partials(tmp_path: &Path) {
    let _ = std::fs::remove_file(tmp_path);
}

/// Drop the file handle and remove the partial temp file.
pub(crate) fn discard_stream(stream: OpenSaveStream) {
    let OpenSaveStream { file, tmp_path, .. } = stream;
    // Drop before remove: Windows refuses `remove_file` while the
    // file handle is open.
    drop(file);
    discard_partials(&tmp_path);
}

/// JS-facing handle to an open save stream. Returned by
/// `file_save_open[_dialog]` and submitted back (as `id`) with each
/// `file_save_write` and the final `file_save_commit` /
/// `file_save_abort`. Mirrors the `SaveStreamHandle` interface in
/// `platformBridge.ts`.
#[derive(Serialize)]
pub(crate) struct SaveStreamHandle {
    pub(crate) id: u64,
    path: String,
}

/// Pick a non-colliding "foo (N).ext" candidate under `dir` and open
/// its `<candidate>.leapmux.tmp` sibling with `create_new`. The partial
/// open serves double duty: it both reserves the iteration spot against
/// concurrent LeapMux saves of the same basename and provides the file
/// the bytes stream into. The candidate itself is skipped if it already
/// exists, preserving the "don't silently overwrite a user file in
/// Downloads" behavior.
///
/// A stale orphaned partial left by a hard process crash is
/// indistinguishable from a live reservation, so it forces "(N)"
/// suffixes on later downloads of the same name until the next launch's
/// `sweep_orphan_tmps` reclaims it (#285). Filenames that themselves end
/// in `SAVE_TMP_SUFFIX` are defused by appending `.download` before the
/// collision loop — otherwise a committed final ending in the suffix
/// would be deleted by the next startup sweep.
pub(crate) fn open_unique_tmp(
    dir: PathBuf,
    filename: String,
) -> Result<(std::fs::File, PathBuf, PathBuf), String> {
    // Defuse reserved-suffix finals: a server-supplied name like
    // `report.leapmux.tmp` would otherwise commit a final the next
    // startup sweep would delete. Collision candidates built from the
    // defused name (`evil.leapmux.tmp (1).download`) never end in the
    // suffix. `file_save_open_dialog` applies the same defuse
    // (`defuse_final_path`) to Save-as targets.
    let filename = if is_partial_name(OsStr::new(&filename)) {
        format!("{filename}{SAVE_DEFUSE_SUFFIX}")
    } else {
        filename
    };
    let as_path = std::path::Path::new(&filename);
    let stem = as_path
        .file_stem()
        .map(|s| s.to_string_lossy().into_owned())
        .unwrap_or_default();
    let ext = as_path
        .extension()
        .map(|s| s.to_string_lossy().into_owned());
    for i in 0..MAX_SAVE_COLLISION_ATTEMPTS {
        let candidate_name = if i == 0 {
            filename.clone()
        } else {
            match &ext {
                Some(e) if !e.is_empty() => format!("{stem} ({i}).{e}"),
                _ => format!("{stem} ({i})"),
            }
        };
        let final_path = dir.join(&candidate_name);
        match final_path.try_exists() {
            Ok(true) => continue,
            Ok(false) => {}
            Err(err) => return Err(format!("stat {candidate_name}: {err}")),
        }
        let tmp_path = tmp_path_for(&final_path);
        match std::fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(&tmp_path)
        {
            Ok(f) => return Ok((f, tmp_path, final_path)),
            Err(err) if err.kind() == std::io::ErrorKind::AlreadyExists => continue,
            Err(err) => return Err(format!("create tmp file: {err}")),
        }
    }
    Err(format!(
        "too many collisions for {filename} (gave up after {MAX_SAVE_COLLISION_ATTEMPTS})"
    ))
}

/// Open a destination in the OS Downloads directory and return a
/// streaming handle. `filename` (from the `filename-b64` header) is
/// sanitized to its basename and collision-dedupped with " (N)".
#[tauri::command]
pub(crate) async fn file_save_open(
    registry: State<'_, Arc<SaveStreamRegistry>>,
    request: tauri::ipc::Request<'_>,
) -> Result<SaveStreamHandle, String> {
    let filename = read_b64_header(&request, "filename-b64")?;
    let downloads = dirs::download_dir().ok_or_else(|| "no downloads directory".to_string())?;
    // Disallow separators in the supplied filename so callers can't
    // escape the Downloads directory.
    let safe_name = std::path::Path::new(&filename)
        .file_name()
        .ok_or_else(|| "invalid filename".to_string())?
        .to_string_lossy()
        .into_owned();
    let registry = registry.inner().clone();
    let (file, tmp_path, final_path) =
        run_blocking(move || open_unique_tmp(downloads, safe_name)).await?;
    Ok(registry.insert(file, tmp_path, final_path))
}

/// Show a native save-as dialog and return a streaming handle for the
/// chosen path. Returns `None` when the user cancels — JS callers
/// should short-circuit before any worker fetch so a cancellation
/// costs nothing.
#[tauri::command]
pub(crate) async fn file_save_open_dialog(
    app: tauri::AppHandle,
    registry: State<'_, Arc<SaveStreamRegistry>>,
    request: tauri::ipc::Request<'_>,
) -> Result<Option<SaveStreamHandle>, String> {
    use tauri_plugin_dialog::DialogExt;

    let default_name = read_b64_header(&request, "default-name-b64")?;
    let (tx, rx) = oneshot::channel();
    app.dialog()
        .file()
        .set_file_name(&default_name)
        .save_file(move |path| {
            let _ = tx.send(path);
        });
    let path_opt = rx.await.map_err(|e| e.to_string())?;
    let Some(file_path) = path_opt else {
        return Ok(None);
    };
    // Defuse a Save-as target whose name ends in the reserved partial
    // suffix so the next startup sweep can't mistake the committed final
    // for an orphan and delete it (#285) -- the same guard `open_unique_tmp`
    // applies to the Downloads flow. This runs after the native dialog's
    // own overwrite prompt, so for such a name the `.download` variant is
    // what actually gets written; it is unconditional (not scoped to
    // Downloads) so no reserved-suffix final ever reaches disk to be swept,
    // whatever the directory's path spelling. Only a name literally ending
    // in `.leapmux.tmp` is affected, which a real download never produces --
    // and if that redirect would land on an existing `.download` the dialog
    // never confirmed, `resolve_save_as_final` errors rather than clobber it.
    let final_path = resolve_save_as_final(file_path.into_path().map_err(|e| e.to_string())?)?;
    let registry = registry.inner().clone();
    let (file, tmp_path) = run_blocking({
        let final_path = final_path.clone();
        move || open_tmp_for_write(&final_path)
    })
    .await?;
    Ok(Some(registry.insert(file, tmp_path, final_path)))
}

/// Append the request body bytes to the open file identified by the
/// decimal `handle-id` header. Uses `block_in_place` rather than
/// `spawn_blocking` so the body slice can be borrowed directly from
/// the request without a per-chunk clone — for a 100 MB save that
/// avoids ~100 MiB of memcpy traffic.
#[tauri::command]
pub(crate) async fn file_save_write(
    registry: State<'_, Arc<SaveStreamRegistry>>,
    request: tauri::ipc::Request<'_>,
) -> Result<(), String> {
    let handle_id = read_handle_id(&request)?;
    let bytes = match request.body() {
        tauri::ipc::InvokeBody::Raw(b) => b.as_slice(),
        _ => return Err("expected raw bytes body".to_string()),
    };
    // Tauri's command executor runs on a multi-thread tokio runtime,
    // so `block_in_place` is safe here: it parks the current worker
    // for the duration of the write and lets the runtime steal other
    // tasks. The write is bounded to one chunk (~1 MiB). The debug
    // assertion makes a future runtime-config regression (e.g. switching
    // to `current_thread`) fail with a clear message instead of tokio's
    // generic "can call `blocking` only from a `MultiThread`" panic.
    debug_assert_eq!(
        tokio::runtime::Handle::current().runtime_flavor(),
        tokio::runtime::RuntimeFlavor::MultiThread,
        "file_save_write uses block_in_place; requires a multi-thread runtime",
    );
    tokio::task::block_in_place(|| registry.write_chunk(handle_id, bytes))
}

/// Finalize the save identified by `handle-id`: sync bytes to disk and
/// atomic-rename the partial onto the final path. Discards partials on
/// failure so a partial sync doesn't leave a junk file under the
/// chosen name.
#[tauri::command]
pub(crate) async fn file_save_commit(
    registry: State<'_, Arc<SaveStreamRegistry>>,
    request: tauri::ipc::Request<'_>,
) -> Result<(), String> {
    let handle_id = read_handle_id(&request)?;
    let registry = registry.inner().clone();
    run_blocking(move || registry.commit(handle_id)).await
}

/// Discard the save identified by `handle-id`: drop the open file and
/// remove the partial. Idempotent against an already-removed
/// handle (e.g. the idle GC raced the JS pump) so the failure path on
/// the JS side stays simple.
#[tauri::command]
pub(crate) async fn file_save_abort(
    registry: State<'_, Arc<SaveStreamRegistry>>,
    request: tauri::ipc::Request<'_>,
) -> Result<(), String> {
    let handle_id = read_handle_id(&request)?;
    let registry = registry.inner().clone();
    run_blocking(move || {
        if let Some(stream) = registry.take(handle_id) {
            discard_stream(stream);
        }
        Ok(())
    })
    .await
}
