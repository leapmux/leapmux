use std::future::Future;
use std::time::Duration;

use crate::proto;

/// The one sidecar shutdown limit of the shell, for a quit and for a dev
/// reclaim alike.
///
/// A slow but orderly quit needs the full sidecar budget -- an agent stop can
/// spend seconds on capture, grace and write timeouts -- so five seconds used
/// to give up on every quit route while the sidecar was still draining. An
/// overrun is safe now: the sidecar survives the shell's exit and finishes
/// releasing the lease on its own, so waiting longer costs a visible window,
/// not a lost lease.
pub(crate) const SIDECAR_SHUTDOWN_TIMEOUT: Duration = Duration::from_secs(30);
const SHUTDOWN_PAUSE: Duration = Duration::from_millis(250);

// request_cleanup waits for the sidecar's cooperative shutdown request.
pub(crate) async fn request_cleanup(
    request: impl Future<Output = Result<proto::Response, String>>,
) -> Result<(), String> {
    let result = match tokio::time::timeout(SIDECAR_SHUTDOWN_TIMEOUT, request).await {
        Ok(Ok(response)) => crate::lifecycle_from_response(response).and_then(|(_, cleanup_errors)| {
            if cleanup_errors.is_empty() {
                Ok(())
            } else {
                Err(format!(
                    "The desktop sidecar reported cleanup errors:\n- {}",
                    cleanup_errors.join("\n- ")
                ))
            }
        }),
        Ok(Err(error)) => Err(format!("The desktop shutdown request failed: {error}")),
        Err(_) => Err(format!(
            "The desktop shutdown reply timed out after {} seconds. Native cleanup completion is unknown.",
            SIDECAR_SHUTDOWN_TIMEOUT.as_secs()
        )),
    };
    tokio::time::sleep(SHUTDOWN_PAUSE).await;
    result
}

#[cfg(test)]
mod tests {
    use super::*;

    fn lifecycle_response(cleanup_errors: Vec<String>) -> proto::Response {
        proto::Response {
            id: 901,
            error: String::new(),
            result: Some(proto::response::Result::Lifecycle(proto::LifecycleResult {
                sidecar_info: Some(proto::SidecarInfo::default()),
                cleanup_errors,
            })),
        }
    }

    #[tokio::test(start_paused = true)]
    async fn accepts_a_completed_shutdown_lifecycle() {
        let result = request_cleanup(async { Ok(lifecycle_response(Vec::new())) }).await;
        assert!(result.is_ok());
    }

    #[tokio::test(start_paused = true)]
    async fn reports_a_shutdown_transport_failure() {
        let result =
            request_cleanup(async { Err("the desktop sidecar disconnected".to_string()) }).await;
        assert!(result.unwrap_err().contains("disconnected"));
    }

    #[tokio::test(start_paused = true)]
    async fn reports_a_shutdown_response_error() {
        let result = request_cleanup(async {
            Ok(proto::Response {
                id: 901,
                error: "the shutdown request was refused".to_string(),
                result: None,
            })
        })
        .await;
        assert!(result.unwrap_err().contains("refused"));
    }

    #[tokio::test(start_paused = true)]
    async fn reports_every_native_cleanup_failure() {
        let result = request_cleanup(async {
            Ok(lifecycle_response(vec![
                "the native engine did not end".to_string(),
                "the runtime lease release failed".to_string(),
            ]))
        })
        .await;
        let error = result.unwrap_err();
        assert!(error.contains("native engine"));
        assert!(error.contains("runtime lease"));
    }

    #[tokio::test(start_paused = true)]
    async fn refuses_an_absent_or_unexpected_lifecycle_result() {
        for result in [
            None,
            Some(proto::response::Result::BoolValue(proto::BoolValue {
                value: true,
            })),
        ] {
            let error = request_cleanup(async {
                Ok(proto::Response {
                    id: 901,
                    error: String::new(),
                    result,
                })
            })
            .await;
            assert!(error.is_err());
        }
        let error = request_cleanup(async {
            Ok(proto::Response {
                id: 901,
                error: String::new(),
                result: Some(proto::response::Result::Lifecycle(proto::LifecycleResult {
                    sidecar_info: None,
                    cleanup_errors: Vec::new(),
                })),
            })
        })
        .await;
        assert!(error.is_err());
    }

    #[tokio::test(start_paused = true)]
    async fn reports_a_shutdown_reply_deadline_without_claiming_cleanup() {
        let started = tokio::time::Instant::now();
        let result =
            request_cleanup(std::future::pending::<Result<proto::Response, String>>()).await;
        let error = result.unwrap_err();
        assert!(error.contains("timed out"));
        assert!(error.contains("completion is unknown"));
        assert_eq!(started.elapsed(), SIDECAR_SHUTDOWN_TIMEOUT + SHUTDOWN_PAUSE);
    }
}
