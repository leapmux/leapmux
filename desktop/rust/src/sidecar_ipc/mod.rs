//! Platform-specific sidecar IPC transport.
//!
//! The desktop shell talks to its sidecar over one of two kernel transports:
//! a Unix domain socket (`unix.rs`) or a Windows named pipe (`windows.rs`).
//! Each platform exposes the SAME surface -- `connect_sidecar_endpoint`,
//! `is_sidecar_gone`, `endpoint_holder_pid`, the dev-endpoint helpers, and the
//! peer-credential checks -- so the connect/handshake/bootstrap logic in
//! `main.rs` is platform-agnostic. The two implementations live as siblings so
//! they are diffable side-by-side, which is exactly what mitigates the drift
//! risk of maintaining cfg-gated twins.
//!
//! Extracted from `main.rs` in <https://github.com/leapmux/leapmux/issues/296>.
//! Distinct from the frame-codec module (`frame.rs`, #282): this layer sits
//! BELOW the wire format and is consumed by it only through the reader/writer
//! halves `connect_sidecar_endpoint` returns.
//!
//! Only the items re-exported below are part of the contract `main.rs` (and its
//! tests) depend on. Internal helpers -- `open_named_pipe_client`/`PipeConnect`
//! and the raw SID/token Win32 wrappers on Windows, the libc `socket_peer_*`
//! building blocks on Unix -- stay module-private so a future connect path
//! cannot reach past the SID-checking seam the way the handshake once did.

#[cfg(unix)]
mod unix;
#[cfg(windows)]
mod windows;

// The connect/handshake/bootstrap surface main.rs uses in production.
#[cfg(unix)]
pub(crate) use unix::{
    cleanup_dev_sidecar_artifacts, connect_sidecar_endpoint, dev_sidecar_endpoint,
    dev_sidecar_metadata_path, endpoint_holder_pid, finalize_sidecar_streams, is_sidecar_gone,
    private_dev_sidecar_endpoint, restrict_dir_permissions, restrict_file_permissions,
    SidecarReader, SidecarWriter,
};
#[cfg(windows)]
pub(crate) use windows::{
    cleanup_dev_sidecar_artifacts, connect_sidecar_endpoint, connect_sidecar_endpoint_async,
    dev_sidecar_endpoint, dev_sidecar_metadata_path, endpoint_holder_pid, is_sidecar_gone,
    pipe_runtime, private_dev_sidecar_endpoint, restrict_dir_permissions,
    restrict_file_permissions, SidecarReader, SidecarWriter, SyncPipeReader, SyncPipeWriter,
};

// Peer-credential / identity primitives the sidecar-IPC tests drive directly
// (binding as another user needs root/admin, so the refusal branch is otherwise
// reachable only in production). Production paths reach these through the
// transport layer, not these imports.
#[cfg(all(unix, test))]
pub(crate) use unix::{require_peer_uid, require_same_user_peer, socket_peer_pid, socket_peer_uid};
#[cfg(all(windows, test))]
pub(crate) use windows::{
    dev_sidecar_metadata_path_in, require_peer_sid, require_same_user_pipe_peer,
    sanitize_sid_for_pipe, sidecar_identity,
};
