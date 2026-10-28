//! HTTP transport compatible with lib/rpc: `POST /rpc/{Service}/{op}` with
//! CBOR bodies, JSON `{"code","message"}` errors, and `GET /healthz`.

use std::sync::Arc;

use axum::Router;
use axum::body::Bytes;
use axum::extract::{DefaultBodyLimit, Path, State};
use axum::http::{HeaderMap, StatusCode, header};
use axum::response::{IntoResponse, Response};
use axum::routing::{get, post};

use crate::api::{self, ReleaseService as _, ServiceError};
use crate::auth::Verifier;
use crate::controller::App;

pub const SERVICE: &str = "ReleaseService";
const OPS: &[&str] = &["create", "get", "list", "rollback", "list-targets"];

pub struct Server {
    pub app: App,
    /// `None` rejects every call, like a nil lib/rpc Verifier.
    pub verifier: Option<Verifier>,
}

pub fn router(s: Arc<Server>) -> Router {
    Router::new()
        .route("/rpc/{service}/{op}", post(call))
        .route("/healthz", get(|| async { "ok\n" }))
        .fallback(|| async { error(400, "expected POST /rpc/{service}/{op}") })
        .layer(DefaultBodyLimit::max(16 << 20))
        .with_state(s)
}

pub fn error(code: i32, message: &str) -> Response {
    let status = StatusCode::from_u16(code as u16).unwrap_or(StatusCode::INTERNAL_SERVER_ERROR);
    let body = serde_json::json!({"code": code, "message": message}).to_string();
    (status, [(header::CONTENT_TYPE, "application/json")], body + "\n").into_response()
}

fn decode<T>(r: Result<T, api::CsilCborError>) -> Result<T, ServiceError> {
    r.map_err(|e| ServiceError { code: 400, message: format!("decode request: {e}") })
}

async fn call(State(s): State<Arc<Server>>, Path((service, op)): Path<(String, String)>, headers: HeaderMap, body: Bytes) -> Response {
    if service != SERVICE || !OPS.contains(&op.as_str()) {
        return error(404, &format!("unknown operation {service}/{op}"));
    }
    let token = headers.get(header::AUTHORIZATION).and_then(|v| v.to_str().ok()).and_then(|v| v.strip_prefix("Bearer "));
    let principal = match (&s.verifier, token) {
        (None, _) => return error(401, "authentication is not configured"),
        (_, None) => return error(401, "bearer token required"),
        (Some(v), Some(t)) => match v.verify(t, (crate::now_ms() / 1000) as i64) {
            Ok(p) => p,
            Err(e) => return error(401, &e.to_string()),
        },
    };
    let start = std::time::Instant::now();
    let app = &s.app;
    let b = &body[..];
    let res: Result<Vec<u8>, ServiceError> = match op.as_str() {
        "create" => decode(api::decode_create_release_request(b)).and_then(|r| app.create(&principal, r)).map(|r| api::encode_release(&r)),
        "get" => decode(api::decode_get_release_request(b)).and_then(|r| app.get(&principal, r)).map(|r| api::encode_release(&r)),
        "list" => decode(api::decode_list_releases_request(b)).and_then(|r| app.list(&principal, r)).map(|r| api::encode_list_releases_response(&r)),
        "rollback" => decode(api::decode_rollback_request(b)).and_then(|r| app.rollback(&principal, r)).map(|r| api::encode_release(&r)),
        _ => decode(api::decode_list_targets_request(b)).and_then(|r| app.list_targets(&principal, r)).map(|r| api::encode_list_targets_response(&r)),
    };
    let code = res.as_ref().err().map_or(0, |e| e.code);
    tracing::info!(op = format!("{service}/{op}"), ms = start.elapsed().as_millis() as u64, code, sub = %principal.sub, "rpc");
    match res {
        Ok(bytes) => ([(header::CONTENT_TYPE, "application/cbor")], bytes).into_response(),
        Err(e) => error(e.code, &e.message),
    }
}
