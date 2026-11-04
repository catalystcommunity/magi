//! The API, compatible with lib/rpc: `POST /rpc/EgressService/{op}` with
//! CBOR bodies, JSON `{"code","message"}` errors, and `GET /healthz`.

use std::sync::Arc;

use axum::Router;
use axum::body::Bytes;
use axum::extract::{DefaultBodyLimit, Path, State};
use axum::http::{HeaderMap, StatusCode, header};
use axum::response::{IntoResponse, Response};
use axum::routing::{get, post};

use crate::api::{self, ServiceError};
use crate::auth::{Principal, Verifier};
use crate::records::Records;

pub const SERVICE: &str = "EgressService";
pub const SCOPE_READ: &str = "egress:read";
pub const SCOPE_ADMIN: &str = "egress:admin";

pub struct Server {
    pub records: Arc<Records>,
    /// `None` rejects every call, like a nil lib/rpc Verifier.
    pub verifier: Option<Verifier>,
}

pub fn router(s: Arc<Server>) -> Router {
    Router::new()
        .route("/rpc/{service}/{op}", post(call))
        .route("/healthz", get(|| async { "ok\n" }))
        .fallback(|| async { error(400, "expected POST /rpc/{service}/{op}") })
        .layer(DefaultBodyLimit::max(32 << 20))
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

fn need(p: &Principal, scope: &str) -> Result<(), ServiceError> {
    if p.has(scope) || p.has(SCOPE_ADMIN) {
        Ok(())
    } else {
        Err(ServiceError { code: 403, message: format!("scope {scope} is required") })
    }
}

fn limit(l: Option<u64>) -> usize {
    l.unwrap_or(100).clamp(1, 1000) as usize
}

impl Server {
    fn list_flows(&self, p: &Principal, r: api::ListFlowsRequest) -> Result<api::ListFlowsResponse, ServiceError> {
        need(p, SCOPE_READ)?;
        Ok(api::ListFlowsResponse { flows: self.records.list(r.environment.as_deref(), r.owner.as_deref(), limit(r.limit)) })
    }
}

async fn call(State(s): State<Arc<Server>>, Path((service, op)): Path<(String, String)>, headers: HeaderMap, body: Bytes) -> Response {
    if service != SERVICE || op != "list-flows" {
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
    let b = &body[..];
    let res: Result<Vec<u8>, ServiceError> =
        decode(api::decode_list_flows_request(b)).and_then(|r| s.list_flows(&principal, r)).map(|r| api::encode_list_flows_response(&r));
    let code = res.as_ref().err().map_or(0, |e| e.code);
    tracing::info!(op = format!("{service}/{op}"), ms = start.elapsed().as_millis() as u64, code, sub = %principal.sub, "rpc");
    match res {
        Ok(bytes) => ([(header::CONTENT_TYPE, "application/cbor")], bytes).into_response(),
        Err(e) => error(e.code, &e.message),
    }
}
