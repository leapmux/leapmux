//! Windows named-pipe sidecar IPC transport.
//!
//! The dev sidecar endpoint is a per-user named pipe. Matching pairs below
//! mirror `unix.rs` so the two OS implementations are diffable side-by-side.
//! This file cannot be compiled or lint-checked on a non-Windows host -- one
//! more reason the twins deserve a dedicated, deliberately-reviewed module.
//!
//! Why tokio's overlapped-I/O client and not a raw CreateFileW/ReadFile/
//! WriteFile wrapper: a named-pipe handle opened without FILE_FLAG_OVERLAPPED
//! serializes all I/O through the FILE_OBJECT lock, even across duplicated
//! handles. A blocked long-lived ReadFile would prevent any concurrent
//! WriteFile from making progress and deadlock the reader/writer threads --
//! the regression pinned by `split_halves_allow_concurrent_read_and_write`
//! in `main.rs`.

use std::io::{self, Read, Write};
use std::path::{Path, PathBuf};
use std::time::Duration;

use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::windows::named_pipe::{ClientOptions, NamedPipeClient};
use windows_sys::Win32::{
    Foundation::{
        CloseHandle, GetLastError, LocalFree, ERROR_FILE_NOT_FOUND, ERROR_INSUFFICIENT_BUFFER,
        ERROR_PIPE_BUSY, HANDLE, HLOCAL,
    },
    Security::{
        Authorization::ConvertSidToStringSidW, EqualSid, GetLengthSid, GetTokenInformation,
        TokenUser, PSID, SECURITY_MAX_SID_SIZE, TOKEN_QUERY, TOKEN_USER,
    },
    System::{
        Pipes::GetNamedPipeServerProcessId,
        Threading::{
            GetCurrentProcess, OpenProcess, OpenProcessToken, PROCESS_QUERY_LIMITED_INFORMATION,
        },
    },
};

pub(crate) type SidecarReader = SyncPipeReader;
pub(crate) type SidecarWriter = SyncPipeWriter;

// `new_multi_thread` with one worker is deliberate: the reader and writer
// threads both call `block_on` on this runtime, and `current_thread` would
// serialize them through the runtime mutex (defeating the parallelism the
// FILE_OBJECT-lock fix exists to enable).
#[cfg(windows)]
pub(crate) fn pipe_runtime() -> &'static tokio::runtime::Runtime {
    static RUNTIME: std::sync::OnceLock<tokio::runtime::Runtime> = std::sync::OnceLock::new();
    RUNTIME.get_or_init(|| {
        tokio::runtime::Builder::new_multi_thread()
            .worker_threads(1)
            .enable_io()
            .enable_time()
            .thread_name("leapmux-named-pipe")
            .build()
            .expect("build named-pipe runtime")
    })
}

#[cfg(windows)]
pub struct SyncPipeReader {
    pub(crate) inner: tokio::io::ReadHalf<NamedPipeClient>,
}

#[cfg(windows)]
impl Read for SyncPipeReader {
    fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
        pipe_runtime().block_on(self.inner.read(buf))
    }
}

#[cfg(windows)]
pub struct SyncPipeWriter {
    pub(crate) inner: tokio::io::WriteHalf<NamedPipeClient>,
}

#[cfg(windows)]
impl Write for SyncPipeWriter {
    fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
        pipe_runtime().block_on(self.inner.write(buf))
    }

    fn flush(&mut self) -> io::Result<()> {
        pipe_runtime().block_on(self.inner.flush())
    }
}

/// Outcome of a named-pipe connect attempt. The THREE states must stay
/// distinct: a pipe that does not exist (`NotFound`) and a pipe whose every
/// server instance is momentarily busy (`Busy`) are opposite facts about the
/// sidecar's liveness -- NotFound means it is gone, Busy means it is alive and
/// serving -- and collapsing both to "no client" (the old `Ok(None)`) let a
/// live-but-busy sidecar read as gone during the shutdown poll, so the shell
/// double-spawned onto an endpoint a zombie still held.
#[cfg(windows)]
enum PipeConnect {
    Connected(NamedPipeClient),
    NotFound,
    Busy,
}

// ERROR_PIPE_BUSY gets a short retry loop; if every instance stays busy across
// the retries the pipe is alive but saturated, reported as Busy (NOT NotFound).
// ERROR_FILE_NOT_FOUND is NotFound; any other error is fatal.
#[cfg(windows)]
async fn open_named_pipe_client(pipe_name: &str) -> Result<PipeConnect, String> {
    const MAX_BUSY_RETRIES: u32 = 3;
    for _ in 0..=MAX_BUSY_RETRIES {
        match ClientOptions::new().open(pipe_name) {
            Ok(client) => return Ok(PipeConnect::Connected(client)),
            Err(err) if err.raw_os_error() == Some(ERROR_FILE_NOT_FOUND as i32) => {
                return Ok(PipeConnect::NotFound);
            }
            Err(err) if err.raw_os_error() == Some(ERROR_PIPE_BUSY as i32) => {
                tokio::time::sleep(Duration::from_millis(50)).await;
                continue;
            }
            Err(err) => return Err(format!("open named pipe {pipe_name}: {err}")),
        }
    }
    // Retries exhausted while every instance was busy: alive, not gone.
    Ok(PipeConnect::Busy)
}

#[cfg(windows)]
pub(crate) fn is_sidecar_gone(pipe_name: &str) -> bool {
    // ONLY NotFound means gone. A Busy pipe is a live sidecar whose instances
    // are all in use -- reporting it gone here is exactly what let the shell
    // abandon a healthy-but-busy sidecar mid-shutdown-poll and double-spawn.
    pipe_runtime().block_on(async {
        matches!(
            open_named_pipe_client(pipe_name).await,
            Ok(PipeConnect::NotFound)
        )
    })
}

/// Refuses a dev sidecar named pipe answered by anyone but this user, the
/// Windows counterpart of the unix `connect_sidecar_endpoint`'s
/// `require_same_user_peer`. See `require_same_user_pipe_peer` for the
/// mechanism and the bind-side pair (`userOnlySDDL` in
/// `backend/locallisten/locallisten_windows.go`) that hardens the *listener*
/// the same way Unix's `requirePrivateDir` does.
///
/// Both this and `endpoint_holder_pid` route through `connect_sidecar_endpoint_async`,
/// the single connect+SID-check core, so the same-user check is structural rather
/// than a convention each caller must remember.
#[cfg(windows)]
pub(crate) fn connect_sidecar_endpoint(
    pipe_name: &str,
) -> Result<Option<(SidecarReader, SidecarWriter)>, String> {
    let client = match pipe_runtime().block_on(connect_sidecar_endpoint_async(pipe_name))? {
        Some(client) => client,
        None => return Ok(None),
    };
    let (r, w) = tokio::io::split(client);
    Ok(Some((
        SyncPipeReader { inner: r },
        SyncPipeWriter { inner: w },
    )))
}

/// Connects `pipe_name` and verifies the server is this user (the same-user SID
/// check), returning the raw `NamedPipeClient` for callers that need async I/O
/// on the client itself -- the dev-sidecar handshake (`windows_handshake_async`),
/// which races the GetSidecarInfo exchange against `tokio::time::timeout` and so
/// cannot use the sync `SyncPipeReader`/`SyncPipeWriter` pair its sibling returns.
///
/// This is the SINGLE connect core both `connect_sidecar_endpoint` (sync, via
/// `pipe_runtime().block_on`) and `endpoint_holder_pid` route through, so the
/// same-user SID check gates EVERY connect that can adopt or query a peer.
/// Everything downstream trusts the peer on its own word (protocol version +
/// binary hash, both forgeable -- see `require_same_user_pipe_peer`), so the
/// check MUST be unavoidable; before this core existed, the handshake and the
/// holder-pid diagnostic both bypassed it, so the Windows dev-sidecar connect
/// paths had NO identity check where Unix had one. The check borrows the client
/// immutably and does no I/O (it reads the kernel-recorded server pid via
/// `AsRawHandle`), so the returned client is fully usable for the exchange that
/// follows.
#[cfg(windows)]
pub(crate) async fn connect_sidecar_endpoint_async(
    pipe_name: &str,
) -> Result<Option<NamedPipeClient>, String> {
    let client = match open_named_pipe_client(pipe_name).await? {
        PipeConnect::Connected(client) => client,
        // Gone: the caller's "try again later / endpoint is free" signal.
        PipeConnect::NotFound => return Ok(None),
        // Alive but saturated. NOT free to take -- surfaced as an error so the
        // bootstrap routes around it onto a private endpoint rather than
        // colliding on an endpoint a live sidecar still holds.
        PipeConnect::Busy => {
            return Err(format!("named pipe {pipe_name} is busy (sidecar alive)"));
        }
    };
    require_same_user_pipe_peer(&client, pipe_name)?;
    Ok(Some(client))
}

#[cfg(windows)]
pub(crate) fn endpoint_holder_pid(endpoint: &str) -> Option<u32> {
    use std::os::windows::io::AsRawHandle;

    // Route through connect_sidecar_endpoint_async so the same-user SID check
    // gates this path too: we only ever want the holder pid of OUR sidecar, and
    // a foreign-user pipe is not that. Gone (NotFound) and busy/unreadable
    // (Err) both mean no pid to report -- the diagnostic falls back to naming
    // no holder.
    let client = pipe_runtime()
        .block_on(connect_sidecar_endpoint_async(endpoint))
        .ok()??;
    let mut pid: u32 = 0;
    let rc = unsafe { GetNamedPipeServerProcessId(client.as_raw_handle() as HANDLE, &mut pid) };
    if rc == 0 || pid == 0 {
        None
    } else {
        Some(pid)
    }
}

#[cfg(windows)]
pub(crate) fn cleanup_dev_sidecar_artifacts(_endpoint: &str, metadata_path: &Path) {
    // Named pipes release themselves when the server closes the listener;
    // only the metadata file persists on disk.
    let _ = std::fs::remove_file(metadata_path);
}

#[cfg(windows)]
pub(crate) fn restrict_dir_permissions(_: &Path) -> Result<(), String> {
    Ok(())
}

#[cfg(windows)]
pub(crate) fn restrict_file_permissions(_: &Path) -> Result<(), String> {
    Ok(())
}

/// Windows counterpart of the unix private endpoint (see `unix.rs`'s
/// `private_dev_sidecar_endpoint` for why a per-PID private endpoint exists at
/// all). Named pipes carry no filesystem path, so the PID goes in the pipe name.
#[cfg(windows)]
pub(crate) fn private_dev_sidecar_endpoint() -> Result<String, String> {
    Ok(format!(
        "\\\\.\\pipe\\leapmux-desktop-{}-sidecar-{}",
        sidecar_identity()?,
        std::process::id()
    ))
}

#[cfg(windows)]
pub(crate) fn dev_sidecar_endpoint() -> Result<String, String> {
    Ok(format!(
        "\\\\.\\pipe\\leapmux-desktop-{}-sidecar",
        sidecar_identity()?
    ))
}

#[cfg(windows)]
pub(crate) fn dev_sidecar_metadata_path() -> PathBuf {
    let base = std::env::var_os("LOCALAPPDATA")
        .map(PathBuf::from)
        .unwrap_or_else(std::env::temp_dir);
    dev_sidecar_metadata_path_in(&base)
}

#[cfg(windows)]
pub(crate) fn dev_sidecar_metadata_path_in(base: &Path) -> PathBuf {
    base.join("leapmux-desktop").join("sidecar.json")
}

#[cfg(windows)]
pub(crate) fn sanitize_sid_for_pipe(raw: &str) -> String {
    raw.chars()
        .map(|c| {
            if c.is_ascii_alphanumeric() || c == '-' {
                c
            } else {
                '_'
            }
        })
        .collect()
}

#[cfg(windows)]
pub(crate) fn sidecar_identity() -> Result<String, String> {
    use std::sync::OnceLock;
    static CACHED: OnceLock<Result<String, String>> = OnceLock::new();
    CACHED
        .get_or_init(|| {
            current_user_sid()
                .and_then(|bytes| sid_to_string(&bytes))
                .map(|raw| sanitize_sid_for_pipe(&raw))
        })
        .clone()
}

/// The current process's user SID, copied into an owned buffer so callers can
/// hold it past the token handle. Used both to name the per-user dev pipe
/// (via `sidecar_identity`) and as the comparison point for the connect-side
/// identity check (`require_same_user_pipe_peer`) -- the two callers that need
/// to agree on what "us" means go through one place to find out.
///
/// The SID is constant for the process's lifetime, so the first successful read
/// is cached and reused. `require_same_user_pipe_peer` runs on the dev-sidecar
/// connect path, which the bootstrap polls every 100ms while waiting for the
/// sidecar to bind (up to `DEV_SIDECAR_CONNECT_TIMEOUT`); an uncached read would
/// re-open and re-query our own process token on every iteration.
#[cfg(windows)]
fn current_user_sid() -> Result<Vec<u8>, String> {
    use std::sync::OnceLock;
    static CACHED: OnceLock<Result<Vec<u8>, String>> = OnceLock::new();
    CACHED
        .get_or_init(|| {
            let mut token: HANDLE = std::ptr::null_mut();
            if unsafe { OpenProcessToken(GetCurrentProcess(), TOKEN_QUERY, &mut token) } == 0 {
                return Err(format!("open process token: error {}", unsafe {
                    GetLastError()
                }));
            }
            let sid = token_user_sid(token);
            unsafe { CloseHandle(token) };
            sid
        })
        .clone()
}

/// Renders a SID (as raw bytes) into the `S-1-5-...` string form. Wraps the
/// `ConvertSidToStringSidW` + `LocalFree` pair so neither caller has to.
#[cfg(windows)]
fn sid_to_string(sid: &[u8]) -> Result<String, String> {
    let mut sid_string_ptr: *mut u16 = std::ptr::null_mut();
    if unsafe { ConvertSidToStringSidW(sid.as_ptr() as PSID, &mut sid_string_ptr) } == 0 {
        return Err(format!("convert sid to string: error {}", unsafe {
            GetLastError()
        }));
    }
    let mut len = 0;
    while unsafe { *sid_string_ptr.add(len) } != 0 {
        len += 1;
    }
    let slice = unsafe { std::slice::from_raw_parts(sid_string_ptr, len) };
    let sid = String::from_utf16_lossy(slice);
    unsafe { LocalFree(sid_string_ptr as HLOCAL) };
    Ok(sid)
}

/// Queries `TokenUser` out of a token handle and returns the SID it points to,
/// copied into an owned buffer.
///
/// `GetTokenInformation` writes a `TOKEN_USER` whose `User.Sid` points *into*
/// the same buffer, so the SID borrows from it; copying into a fresh `Vec<u8>`
/// hands the caller a SID whose lifetime is independent of the token query.
/// `GetLengthSid` reports the kernel's byte count for a valid SID, so the copy
/// is exactly sized.
///
/// The size-probe call is EXPECTED to fail with ERROR_INSUFFICIENT_BUFFER (that
/// is how it reports the required size in `needed`). Checking its return
/// distinguishes that expected failure from a real one: without the check, any
/// other failure leaves `needed` at 0, the second call runs against an empty
/// buffer, and the error reported below is the SECOND call's -- masking the
/// true cause (a bad token handle, a denied query).
#[cfg(windows)]
fn token_user_sid(token: HANDLE) -> Result<Vec<u8>, String> {
    let mut needed: u32 = 0;
    if unsafe { GetTokenInformation(token, TokenUser, std::ptr::null_mut(), 0, &mut needed) } == 0 {
        let probe_err = unsafe { GetLastError() };
        if probe_err != ERROR_INSUFFICIENT_BUFFER {
            return Err(format!("probe token user info size: error {probe_err}"));
        }
    }
    let mut buffer = vec![0u8; needed as usize];
    let ok = unsafe {
        GetTokenInformation(
            token,
            TokenUser,
            buffer.as_mut_ptr() as *mut _,
            needed,
            &mut needed,
        )
    };
    let token_err = if ok == 0 {
        unsafe { GetLastError() }
    } else {
        0
    };
    if ok == 0 {
        return Err(format!("get token user info: error {token_err}"));
    }
    let user_info = unsafe { &*(buffer.as_ptr() as *const TOKEN_USER) };
    let sid_ptr = user_info.User.Sid;
    let len = unsafe { GetLengthSid(sid_ptr) } as usize;
    if len == 0 {
        return Err("token user sid: GetLengthSid returned 0".to_string());
    }
    // Cap the allocation off the SDK's own SID ceiling. SECURITY_MAX_SID_SIZE (68)
    // is the maximum byte length of any valid SID the kernel ever produces, so 4x
    // it is far above any legitimate SID and far below a dangerous allocation.
    // GetLengthSid reads kernel-recorded data for a valid SID, but this whole chain
    // is reached on the connect path off a peer-controlled pipe name, so a
    // malformed/huge length must fail closed rather than drive a multi-GiB vec!.
    const MAX_SID_BYTES: usize = SECURITY_MAX_SID_SIZE as usize * 4;
    if len > MAX_SID_BYTES {
        return Err(format!(
            "token user sid: GetLengthSid returned {len} bytes (max {MAX_SID_BYTES})"
        ));
    }
    let mut sid = vec![0u8; len];
    unsafe { std::ptr::copy_nonoverlapping(sid_ptr as *const u8, sid.as_mut_ptr(), len) };
    Ok(sid)
}

/// Reads the server process's user SID from a connected named-pipe client
/// handle. The PID is the kernel's report about which process bound this pipe
/// instance (not a value the peer asserts over the wire), and the SID is read
/// from that PID's primary token -- so the same chain of trust as Unix's
/// `SO_PEERCRED` / `getpeereid`: a fact the peer cannot forge.
///
/// `PROCESS_QUERY_LIMITED_INFORMATION` is the least-privilege access right
/// that lets us read another process's token's user; same-user processes hold
/// it by default, and any failure here fails closed in the caller.
#[cfg(windows)]
fn pipe_peer_sid(client: &NamedPipeClient) -> Result<Vec<u8>, String> {
    use std::os::windows::io::AsRawHandle;

    let mut server_pid: u32 = 0;
    if unsafe { GetNamedPipeServerProcessId(client.as_raw_handle() as HANDLE, &mut server_pid) }
        == 0
    {
        return Err(format!("query named pipe server pid: error {}", unsafe {
            GetLastError()
        }));
    }
    let process = unsafe { OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, 0, server_pid) };
    if process.is_null() {
        return Err(format!(
            "open server process {server_pid}: error {}",
            unsafe { GetLastError() }
        ));
    }
    let mut token: HANDLE = std::ptr::null_mut();
    let token_rc = unsafe { OpenProcessToken(process, TOKEN_QUERY, &mut token) };
    unsafe { CloseHandle(process) };
    if token_rc == 0 {
        return Err(format!("open server process token: error {}", unsafe {
            GetLastError()
        }));
    }
    let sid = token_user_sid(token);
    unsafe { CloseHandle(token) };
    sid
}

/// The refusal decision itself, split from the pipe so it can be tested
/// against a foreign SID -- binding a pipe as another user needs admin, so the
/// branch that actually matters here is otherwise reachable only in production.
/// Mirrors `require_peer_uid` on Unix.
#[cfg(windows)]
pub(crate) fn require_peer_sid(peer: &[u8], ours: &[u8], pipe_name: &str) -> Result<(), String> {
    // EqualSid is the canonical Win32 SID comparison; it returns FALSE for
    // unequal SIDs (different lengths or different bytes), so a peer whose SID
    // differs in any way is a clean refusal rather than a panic.
    let equal = unsafe { EqualSid(peer.as_ptr() as PSID, ours.as_ptr() as PSID) } != 0;
    if !equal {
        return Err(format!(
            "refusing sidecar at {pipe_name}: it is answered by a different user; \
             something else is holding this endpoint"
        ));
    }
    Ok(())
}

/// Refuses a dev sidecar named pipe answered by anyone but this user. Windows
/// counterpart of Unix's `require_same_user_peer`; see that function for why
/// the connect side must check this even though the Go side already restricts
/// the bind to our SID (via `userOnlySDDL` in locallisten_windows.go).
#[cfg(windows)]
pub(crate) fn require_same_user_pipe_peer(
    client: &NamedPipeClient,
    pipe_name: &str,
) -> Result<(), String> {
    let peer_sid = pipe_peer_sid(client)?;
    let our_sid = current_user_sid()?;
    require_peer_sid(&peer_sid, &our_sid, pipe_name)
}
