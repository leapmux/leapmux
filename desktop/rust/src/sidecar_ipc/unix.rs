//! Unix-domain-socket sidecar IPC transport.
//!
//! The dev sidecar endpoint is a per-user socket at a predictable path. Matching
//! pairs below mirror `windows.rs` so the two OS implementations are diffable
//! side-by-side.

use std::io;
#[cfg(unix)]
use std::os::unix::net::UnixStream;
use std::path::{Path, PathBuf};

use crate::DEV_SIDECAR_HANDSHAKE_TIMEOUT;

#[cfg(unix)]
pub(crate) type SidecarReader = UnixStream;
#[cfg(unix)]
pub(crate) type SidecarWriter = UnixStream;

/// A dev sidecar endpoint private to THIS shell process.
///
/// The shared per-user endpoint is what lets a dev reload reuse a running sidecar, so
/// it is the default. But it is only a cache: when it cannot be reclaimed -- a wedged
/// leftover that ignores a cooperative shutdown, or another user's socket -- the
/// launch must still succeed. Suffixing our own PID gives a path nothing else holds,
/// so a fresh sidecar always starts. In LAUNCHER mode a single unkillable orphan then
/// costs only the reuse optimisation. In SOLO mode it costs more: the orphan also
/// holds the shared user-data DB's runtime lease, so the fresh sidecar's ConnectSolo
/// fails against the locked DB until the orphan is stopped by hand (surfaced by the
/// `request_sidecar_shutdown` diagnostic, which names the kernel-verified holder pid).
///
/// The previous behaviour, aborting the launch, made one leftover from a SIGKILLed
/// `task test-e2e` run block every subsequent `task dev` until the dev hunted it down
/// by hand.
#[cfg(unix)]
pub(crate) fn private_dev_sidecar_endpoint() -> String {
    dev_sidecar_runtime_dir()
        .join(format!(
            "{}-sidecar-{}.sock",
            sidecar_identity(),
            std::process::id()
        ))
        .display()
        .to_string()
}

#[cfg(unix)]
/// Refuses a dev sidecar socket answered by anyone but this user.
///
/// Everything downstream of the connect trusts the peer on its own word: the shell
/// adopts whatever answers here as its sidecar if it self-reports a matching protocol
/// version and binary hash — and a hash is exactly as forgeable as the PID that
/// `force_kill_sidecar` used to trust before it was deleted for that reason. The
/// endpoint sits at a predictable path (the shell derives it from
/// `std::env::temp_dir()`), so "whoever bound it first" is not an authorization.
///
/// The Go side hardens the *bind* (see `requirePrivateDir` in
/// desktop/go/socket_unix.go, which refuses a socket dir it does not own): this is
/// the same boundary from the connect side, and it must be checked here too, because
/// an honest sidecar refusing to bind a squatted directory does nothing to stop this
/// shell from connecting to whatever a squatter bound instead.
///
/// `peer_cred` reads the credentials the KERNEL recorded for the peer, so unlike the
/// hash it is not something the peer can assert. Dev-only, like the endpoint itself:
/// a bundled build spawns its own child over stdio pipes and never comes here.
#[cfg(unix)]
pub(crate) fn require_same_user_peer(stream: &UnixStream, endpoint: &str) -> Result<(), String> {
    require_peer_uid(
        socket_peer_uid(stream)?,
        unsafe { libc::getuid() },
        endpoint,
    )
}

/// The refusal decision itself, split from the socket so it can be tested against a
/// foreign uid — binding a socket as another user needs root, so the branch that
/// actually matters here is otherwise reachable only in production.
#[cfg(unix)]
pub(crate) fn require_peer_uid(peer_uid: u32, our_uid: u32, endpoint: &str) -> Result<(), String> {
    if peer_uid != our_uid {
        return Err(format!(
            "refusing sidecar at {endpoint}: it is answered by uid {peer_uid}, not {our_uid}; \
             something else is holding this endpoint"
        ));
    }
    Ok(())
}

/// Reads the peer's uid from a connected Unix socket.
///
/// std's `UnixStream::peer_cred` is still unstable, so this goes to libc. The two
/// families expose the same fact through different calls: Linux via the `SO_PEERCRED`
/// socket option, macOS/BSD via `getpeereid(3)`.
#[cfg(all(unix, target_os = "linux"))]
pub(crate) fn socket_peer_uid(stream: &UnixStream) -> Result<u32, String> {
    use std::os::fd::AsRawFd;

    let mut cred = libc::ucred {
        pid: 0,
        uid: 0,
        gid: 0,
    };
    let mut len = std::mem::size_of::<libc::ucred>() as libc::socklen_t;
    // SAFETY: `cred` and `len` are live, correctly sized, and only written by the
    // kernel on success; the fd is owned by `stream` for the duration of the call.
    let rc = unsafe {
        libc::getsockopt(
            stream.as_raw_fd(),
            libc::SOL_SOCKET,
            libc::SO_PEERCRED,
            std::ptr::from_mut(&mut cred).cast::<libc::c_void>(),
            &mut len,
        )
    };
    if rc != 0 {
        return Err(format!(
            "read sidecar socket peer credentials: {}",
            io::Error::last_os_error()
        ));
    }
    Ok(cred.uid)
}

#[cfg(all(unix, not(target_os = "linux")))]
pub(crate) fn socket_peer_uid(stream: &UnixStream) -> Result<u32, String> {
    use std::os::fd::AsRawFd;

    let mut uid: libc::uid_t = 0;
    let mut gid: libc::gid_t = 0;
    // SAFETY: both out-params are live for the call and only written on success; the
    // fd is owned by `stream` for the duration.
    let rc = unsafe { libc::getpeereid(stream.as_raw_fd(), &mut uid, &mut gid) };
    if rc != 0 {
        return Err(format!(
            "read sidecar socket peer credentials: {}",
            io::Error::last_os_error()
        ));
    }
    Ok(uid)
}

/// The KERNEL-recorded pid of the socket peer -- the process actually on the
/// other end, not a pid it reported about itself over the wire. Used only to
/// make the "an orphan is holding the endpoint" diagnostic actionable
/// (`request_sidecar_shutdown`); it is NOT an authorization signal and nothing
/// is killed by it. Linux reads it from the `SO_PEERCRED` ucred whose uid we
/// already consult; macOS/BSD from `LOCAL_PEERPID`. Returns None on any error
/// rather than failing the caller: a missing pid only weakens a log line.
#[cfg(all(unix, target_os = "linux"))]
pub(crate) fn socket_peer_pid(stream: &UnixStream) -> Option<u32> {
    use std::os::fd::AsRawFd;

    let mut cred = libc::ucred {
        pid: 0,
        uid: 0,
        gid: 0,
    };
    let mut len = std::mem::size_of::<libc::ucred>() as libc::socklen_t;
    // SAFETY: as in socket_peer_uid -- kernel writes `cred`/`len` on success; the
    // fd is owned by `stream` for the call.
    let rc = unsafe {
        libc::getsockopt(
            stream.as_raw_fd(),
            libc::SOL_SOCKET,
            libc::SO_PEERCRED,
            std::ptr::from_mut(&mut cred).cast::<libc::c_void>(),
            &mut len,
        )
    };
    if rc != 0 || cred.pid <= 0 {
        return None;
    }
    Some(cred.pid as u32)
}

#[cfg(all(unix, not(target_os = "linux")))]
pub(crate) fn socket_peer_pid(stream: &UnixStream) -> Option<u32> {
    use std::os::fd::AsRawFd;

    let mut pid: libc::pid_t = 0;
    let mut len = std::mem::size_of::<libc::pid_t>() as libc::socklen_t;
    // SAFETY: `pid`/`len` are live for the call and only written on success; the fd
    // is owned by `stream`. LOCAL_PEERPID reports the kernel-recorded peer pid.
    let rc = unsafe {
        libc::getsockopt(
            stream.as_raw_fd(),
            libc::SOL_LOCAL,
            libc::LOCAL_PEERPID,
            std::ptr::from_mut(&mut pid).cast::<libc::c_void>(),
            &mut len,
        )
    };
    if rc != 0 || pid <= 0 {
        return None;
    }
    Some(pid as u32)
}

/// The kernel-verified pid holding `endpoint`, for diagnostics only (see
/// socket_peer_pid). On unix it opens a throwaway connection and reads the
/// peer pid; on Windows it does the same through `GetNamedPipeServerProcessId`
/// on a freshly opened client handle.
#[cfg(unix)]
pub(crate) fn endpoint_holder_pid(endpoint: &str) -> Option<u32> {
    let stream = UnixStream::connect(endpoint).ok()?;
    socket_peer_pid(&stream)
}

// The sidecar-IPC layer exists as per-platform twins (unix socket vs Windows
// named pipe) grouped here and in windows.rs so the two OS implementations are
// diffable side-by-side. See https://github.com/leapmux/leapmux/issues/296
// (distinct from the frame-codec extraction in #282).
#[cfg(unix)]
pub(crate) fn connect_sidecar_endpoint(
    endpoint: &str,
) -> Result<Option<(SidecarReader, SidecarWriter)>, String> {
    let stream = match UnixStream::connect(endpoint) {
        Ok(stream) => stream,
        Err(err)
            if err.kind() == io::ErrorKind::NotFound
                || err.kind() == io::ErrorKind::ConnectionRefused =>
        {
            return Ok(None);
        }
        Err(err) => return Err(format!("connect desktop sidecar socket: {err}")),
    };
    require_same_user_peer(&stream, endpoint)?;
    let reader = stream
        .try_clone()
        .map_err(|err| format!("clone sidecar socket: {err}"))?;
    let writer = stream;
    writer
        .set_write_timeout(Some(DEV_SIDECAR_HANDSHAKE_TIMEOUT))
        .map_err(|err| format!("set sidecar socket write timeout: {err}"))?;
    reader
        .set_read_timeout(Some(DEV_SIDECAR_HANDSHAKE_TIMEOUT))
        .map_err(|err| format!("set sidecar socket read timeout: {err}"))?;
    Ok(Some((reader, writer)))
}

// Handshake timeouts must be cleared before streams are handed to the
// long-lived reader thread; otherwise reads fail with EAGAIN after a few
// seconds of idle and tear the connection down.
#[cfg(unix)]
pub(crate) fn finalize_sidecar_streams(
    reader: &SidecarReader,
    writer: &SidecarWriter,
) -> Result<(), String> {
    reader
        .set_read_timeout(None)
        .map_err(|err| format!("clear sidecar socket read timeout: {err}"))?;
    writer
        .set_write_timeout(None)
        .map_err(|err| format!("clear sidecar socket write timeout: {err}"))?;
    Ok(())
}

#[cfg(unix)]
pub(crate) fn is_sidecar_gone(endpoint: &str) -> bool {
    !Path::new(endpoint).exists()
}

#[cfg(unix)]
pub(crate) fn cleanup_dev_sidecar_artifacts(endpoint: &str, metadata_path: &Path) {
    let _ = std::fs::remove_file(endpoint);
    let _ = std::fs::remove_file(metadata_path);
}

#[cfg(unix)]
pub(crate) fn restrict_dir_permissions(path: &Path) -> Result<(), String> {
    use std::os::unix::fs::PermissionsExt;
    std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o700))
        .map_err(|err| format!("set sidecar metadata dir permissions: {err}"))
}

#[cfg(unix)]
pub(crate) fn restrict_file_permissions(path: &Path) -> Result<(), String> {
    use std::os::unix::fs::PermissionsExt;
    std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o600))
        .map_err(|err| format!("set sidecar metadata permissions: {err}"))
}

#[cfg(unix)]
pub(crate) fn dev_sidecar_endpoint() -> String {
    dev_sidecar_runtime_dir()
        .join(format!("{}-sidecar.sock", sidecar_identity()))
        .display()
        .to_string()
}

#[cfg(unix)]
pub(crate) fn dev_sidecar_metadata_path() -> PathBuf {
    dev_sidecar_runtime_dir().join(format!("{}-sidecar.json", sidecar_identity()))
}

#[cfg(unix)]
fn dev_sidecar_runtime_dir() -> PathBuf {
    std::env::temp_dir().join("leapmux-desktop")
}

#[cfg(unix)]
pub(crate) fn sidecar_identity() -> String {
    use std::sync::OnceLock;
    static CACHED: OnceLock<String> = OnceLock::new();
    CACHED
        .get_or_init(|| {
            std::env::var("USER")
                .or_else(|_| std::env::var("USERNAME"))
                .unwrap_or_else(|_| "default".to_string())
                .chars()
                .map(|ch| if ch.is_ascii_alphanumeric() { ch } else { '_' })
                .collect()
        })
        .clone()
}
