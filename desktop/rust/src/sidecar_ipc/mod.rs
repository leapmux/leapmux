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

#[cfg(unix)]
mod unix;
#[cfg(windows)]
mod windows;

#[cfg(unix)]
pub(crate) use unix::*;
#[cfg(windows)]
pub(crate) use windows::*;
