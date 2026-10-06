use codex_planner_host::{
    HostError, MAX_FRAME, decode_request, infer, planner_config, runtime_auth, host_runtime,
};
use tokio::io::{AsyncReadExt, AsyncWriteExt};

// Single JSONL request/response. EOF cancels inference; a second frame is invalid.
// No arguments, environment flags or request fields can change the tool policy.
fn main() {
    let runtime=host_runtime().expect("host runtime unavailable");
    if runtime.block_on(runtime.spawn(async_main())).is_err() {std::process::exit(1)}
}
async fn async_main() {
    let outcome = tokio::time::timeout(std::time::Duration::from_secs(60), run())
        .await
        .unwrap_or(Err(HostError::Timeout));
    let (response, failed) = match outcome {
        Ok(output) => (
            serde_json::json!({"version":1,"structuredOutput":serde_json::from_str::<serde_json::Value>(&output).unwrap()}),
            false,
        ),
        Err(error) => (serde_json::json!({"version":1,"error":error}), true),
    };
    let mut bytes = serde_json::to_vec(&response).unwrap();
    bytes.push(b'\n');
    let mut stdout = tokio::io::stdout();
    let write_failed = stdout.write_all(&bytes).await.is_err() || stdout.flush().await.is_err();
    // Tokio stdin uses a blocking read which abort cannot interrupt. Core has
    // already terminated here; exit avoids waiting forever on that stdin worker.
    std::process::exit(if failed || write_failed { 1 } else { 0 })
}
async fn run() -> Result<String, HostError> {
    if std::env::args_os().len() != 1 {
        return Err(HostError::InvalidRequest);
    }
    let mut stdin = tokio::io::stdin();
    let mut frame = Vec::new();
    loop {
        let byte = stdin
            .read_u8()
            .await
            .map_err(|_| HostError::InvalidRequest)?;
        if byte == b'\n' {
            break;
        }
        if frame.len() == MAX_FRAME {
            return Err(HostError::InputLimit);
        }
        frame.push(byte);
    }
    let request = decode_request(&frame)?;
    let config = planner_config(
        std::path::Path::new("/run/codex-auth"),
        request.instructions(),
    )
    .await?;
    let auth = runtime_auth(&config).await?;
    let (tx, rx) = tokio::sync::oneshot::channel();
    let monitor = tokio::spawn(async move {
        let error = match stdin.read_u8().await {
            Ok(_) => HostError::InvalidRequest,
            Err(_) => HostError::Cancelled,
        };
        let _ = tx.send(error);
    });
    tokio::pin!(rx);
    let mut cancellation_error = None;
    let cancel = async {
        cancellation_error = Some((&mut rx).await.unwrap_or(HostError::Cancelled));
    };
    let result = infer(request, config, auth, cancel).await;
    monitor.abort();
    if let Some(error) = cancellation_error {
        return Err(error);
    }
    result
}
