//! Calls from the proxy and its plugins to Magi (EgressAskService in
//! contracts/magi.csil). The proxy adds "plugin" and "flow" to each plugin
//! request, so a plugin cannot speak for another plugin or another flow.

use std::collections::HashMap;
use std::path::PathBuf;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use bytes::Bytes;
use ciborium::Value;
use http_body_util::{BodyExt as _, Full};

use crate::net::Client;
use crate::plugins::{Direction, Flow};

/// The most body bytes that go to Magi with a flow.
pub const FLOW_BODY: usize = 8 << 10;
const SERVICE: &str = "EgressAskService";
const ENV_TTL: Duration = Duration::from_secs(300);
/// How long the proxy keeps an answer to `destination`, and how long it
/// sends no second report for one destination of one environment.
pub const DEST_TTL: Duration = Duration::from_secs(60);

#[derive(Debug, Clone, Default, PartialEq)]
pub struct EnvInfo {
    pub owner: String,
    pub task: String,
    pub goal: String,
}

/// What a destination may do.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Access {
    No,
    /// HTTP and TLS, inspected.
    Inspect,
    /// Bytes only, not inspected (protocols that the proxy does not end).
    Layer4,
}

pub struct Magi {
    base: String,
    token_file: PathBuf,
    client: Client,
    envs: Mutex<HashMap<String, (Instant, EnvInfo)>>,
    dests: Mutex<HashMap<String, (Instant, Access)>>,
    reported: Mutex<HashMap<(String, String), Instant>>,
}

impl Magi {
    pub fn new(base: &str, token_file: PathBuf, client: Client) -> Self {
        Magi {
            base: base.trim_end_matches('/').to_string(),
            token_file,
            client,
            envs: Mutex::new(HashMap::new()),
            dests: Mutex::new(HashMap::new()),
            reported: Mutex::new(HashMap::new()),
        }
    }

    async fn call(&self, op: &str, body: Vec<u8>) -> Result<Vec<u8>, String> {
        let token = std::fs::read_to_string(&self.token_file).map_err(|e| format!("read the Magi token: {e}"))?;
        let req = http::Request::post(format!("{}/rpc/{SERVICE}/{op}", self.base))
            .header(http::header::CONTENT_TYPE, "application/cbor")
            .header(http::header::AUTHORIZATION, format!("Bearer {}", token.trim()))
            .body(Full::new(Bytes::from(body)))
            .map_err(|e| e.to_string())?;
        let res = tokio::time::timeout(Duration::from_secs(60), self.client.request(req))
            .await
            .map_err(|_| "Magi did not answer in 60 s".to_string())?
            .map_err(|e| format!("Magi: {e}"))?;
        let status = res.status();
        let body = res.into_body().collect().await.map_err(|e| e.to_string())?.to_bytes();
        if !status.is_success() {
            let msg = serde_json::from_slice::<serde_json::Value>(&body)
                .ok()
                .and_then(|v| v["message"].as_str().map(str::to_string))
                .unwrap_or_else(|| String::from_utf8_lossy(&body).into_owned());
            return Err(format!("Magi {op}: {status}: {msg}"));
        }
        Ok(body.to_vec())
    }

    /// A plugin call. `request` must be a CBOR map.
    pub async fn ask(&self, op: &str, plugin: &str, flow: &Flow, request: &[u8]) -> Result<Vec<u8>, String> {
        let body = with_context(request, plugin, flow)?;
        self.call(op, body).await
    }

    /// The owner and the task of an environment. Kept for some minutes.
    pub async fn environment(&self, env: &str) -> Result<EnvInfo, String> {
        if let Some((at, info)) = self.envs.lock().unwrap().get(env)
            && at.elapsed() < ENV_TTL
        {
            return Ok(info.clone());
        }
        let req = map(vec![("environment", Value::Text(env.into()))]);
        let res = self.call("environment", encode(&req)).await?;
        let v: Value = ciborium::from_reader(&res[..]).map_err(|e| format!("decode environment: {e}"))?;
        let info = EnvInfo { owner: text(&v, "owner"), task: text(&v, "task"), goal: text(&v, "goal") };
        self.envs.lock().unwrap().insert(env.to_string(), (Instant::now(), info.clone()));
        Ok(info)
    }
}

impl Magi {
    /// What a decided review permits for a destination (host:port) that
    /// the policy does not permit. An error is No (fail closed). The answer
    /// is kept for DEST_TTL.
    pub async fn destination(&self, env: &str, dest: &str) -> Access {
        if let Some((at, a)) = self.dests.lock().unwrap().get(dest)
            && at.elapsed() < DEST_TTL
        {
            return *a;
        }
        let req = map(vec![("environment", Value::Text(env.into())), ("destination", Value::Text(dest.into()))]);
        let a = match self.call("destination", encode(&req)).await {
            Ok(res) => match ciborium::from_reader::<Value, _>(&res[..]) {
                Ok(v) if flag(&v, "allowed") && flag(&v, "layer4") => Access::Layer4,
                Ok(v) if flag(&v, "allowed") => Access::Inspect,
                _ => Access::No,
            },
            Err(e) => {
                tracing::warn!(destination = %dest, error = %e, "Magi did not answer for a destination");
                Access::No
            }
        };
        self.dests.lock().unwrap().insert(dest.to_string(), (Instant::now(), a));
        a
    }

    /// True once for each environment and destination in DEST_TTL: the
    /// proxy reports a refused destination only then.
    pub fn first_report(&self, env: &str, dest: &str) -> bool {
        let mut r = self.reported.lock().unwrap();
        r.retain(|_, at| at.elapsed() < DEST_TTL);
        let key = (env.to_string(), dest.to_string());
        if r.contains_key(&key) {
            return false;
        }
        r.insert(key, Instant::now());
        true
    }

    /// Tells Magi that the proxy blocked a flow for review. kind is
    /// "request" or "destination". Returns the review id.
    pub async fn report(&self, kind: &str, flow: &Flow, reason: &str, by: &str) -> Result<String, String> {
        let req = map(vec![
            ("kind", Value::Text(kind.into())),
            ("flow", flow_value(flow)),
            ("reason", Value::Text(reason.into())),
            ("by", Value::Text(by.into())),
        ]);
        let res = self.call("report", encode(&req)).await?;
        let v: Value = ciborium::from_reader(&res[..]).map_err(|e| format!("decode report: {e}"))?;
        Ok(text(&v, "review"))
    }
}

/// Sends a report in the background, with a time limit, so that the client
/// does not wait for Magi.
pub fn spawn_report(magi: &Arc<Magi>, kind: &'static str, flow: Flow, reason: String, by: String) {
    let m = magi.clone();
    tokio::spawn(async move {
        match tokio::time::timeout(Duration::from_secs(20), m.report(kind, &flow, &reason, &by)).await {
            Ok(Ok(id)) => tracing::info!(kind, review = %id, destination = %flow.destination, "reported for review"),
            Ok(Err(e)) => tracing::warn!(kind, destination = %flow.destination, error = %e, "report for review failed"),
            Err(_) => tracing::warn!(kind, destination = %flow.destination, "report for review timed out"),
        }
    });
}

/// Puts "plugin" and "flow" in the request map, in place of any keys with
/// those names.
pub fn with_context(request: &[u8], plugin: &str, flow: &Flow) -> Result<Vec<u8>, String> {
    let v: Value = if request.is_empty() { Value::Map(vec![]) } else { ciborium::from_reader(request).map_err(|e| format!("request is not CBOR: {e}"))? };
    let Value::Map(mut m) = v else { return Err("request must be a CBOR map".into()) };
    m.retain(|(k, _)| !matches!(k.as_text(), Some("plugin" | "flow")));
    m.push((Value::Text("plugin".into()), Value::Text(plugin.into())));
    m.push((Value::Text("flow".into()), flow_value(flow)));
    Ok(encode(&Value::Map(m)))
}

pub fn flow_value(f: &Flow) -> Value {
    let headers = f
        .headers
        .iter()
        .map(|h| map(vec![("name", Value::Text(h.name.clone())), ("value", Value::Text(h.value.clone()))]))
        .collect();
    map(vec![
        ("id", Value::Text(f.id.clone())),
        ("environment", Value::Text(f.environment.clone())),
        ("owner", Value::Text(f.owner.clone())),
        ("direction", Value::Text(match f.direction { Direction::Out => "out", Direction::In => "in" }.into())),
        ("destination", Value::Text(f.destination.clone())),
        ("method", Value::Text(f.method.clone())),
        ("url", Value::Text(f.url.clone())),
        ("status", Value::Integer(f.status.into())),
        ("headers", Value::Array(headers)),
        ("body", Value::Bytes(f.body[..f.body.len().min(FLOW_BODY)].to_vec())),
    ])
}

fn map(kv: Vec<(&str, Value)>) -> Value {
    Value::Map(kv.into_iter().map(|(k, v)| (Value::Text(k.into()), v)).collect())
}

pub fn encode(v: &Value) -> Vec<u8> {
    let mut out = Vec::new();
    ciborium::into_writer(v, &mut out).expect("CBOR encode");
    out
}

fn flag(v: &Value, key: &str) -> bool {
    v.as_map()
        .and_then(|m| m.iter().find(|(k, _)| k.as_text() == Some(key)))
        .and_then(|(_, v)| v.as_bool())
        .unwrap_or(false)
}

fn text(v: &Value, key: &str) -> String {
    v.as_map()
        .and_then(|m| m.iter().find(|(k, _)| k.as_text() == Some(key)))
        .and_then(|(_, v)| v.as_text())
        .unwrap_or_default()
        .to_string()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::plugins::Header;

    fn flow() -> Flow {
        Flow {
            id: "flw_1".into(),
            environment: "env_a".into(),
            owner: "user:a@example.local".into(),
            direction: Direction::Out,
            destination: "api.example.local:443".into(),
            method: "POST".into(),
            url: "https://api.example.local/x".into(),
            status: 0,
            headers: vec![Header { name: "accept".into(), value: "*/*".into() }],
            body: vec![b'x'; FLOW_BODY + 10],
        }
    }

    #[test]
    fn plugin_cannot_replace_the_context() {
        let req = map(vec![("question", Value::Text("q".into())), ("plugin", Value::Text("other".into())), ("flow", Value::Null)]);
        let out = with_context(&encode(&req), "review", &flow()).unwrap();
        let v: Value = ciborium::from_reader(&out[..]).unwrap();
        let m = v.as_map().unwrap();
        assert_eq!(m.len(), 3);
        assert_eq!(text(&v, "plugin"), "review");
        assert_eq!(text(&v, "question"), "q");
        let f = &m.iter().find(|(k, _)| k.as_text() == Some("flow")).unwrap().1;
        assert_eq!(text(f, "environment"), "env_a");
        let body = f.as_map().unwrap().iter().find(|(k, _)| k.as_text() == Some("body")).unwrap().1.as_bytes().unwrap().len();
        assert_eq!(body, FLOW_BODY);
        assert!(with_context(&encode(&Value::Array(vec![])), "p", &flow()).is_err());
        assert!(with_context(b"\xff", "p", &flow()).is_err());
        assert!(with_context(b"", "p", &flow()).is_ok());
    }

    /// No answer from Magi means "not permitted"; a second report for one
    /// environment and destination waits a minute.
    #[tokio::test]
    async fn destination_fails_closed_and_reports_once() {
        let dir = tempfile::tempdir().unwrap();
        let tok = dir.path().join("t");
        std::fs::write(&tok, "x").unwrap();
        let m = Magi::new("http://127.0.0.1:1", tok, crate::net::client(crate::net::roots(None).unwrap()).unwrap());
        assert_eq!(m.destination("env_a", "x.example.local:443").await, Access::No);
        assert!(m.first_report("env_a", "x.example.local:443"));
        assert!(!m.first_report("env_a", "x.example.local:443"));
        assert!(m.first_report("env_b", "x.example.local:443"));
    }

    /// Prints the CBOR that Magi gets, for the Go decoder test
    /// (magi/compose/egress_test.go, egressFixture).
    #[test]
    fn go_fixture() {
        let mut f = flow();
        f.body = b"hi".to_vec();
        let req = map(vec![("question", Value::Text("q".into())), ("extra", Value::Integer(7.into()))]);
        println!("FIXTURE {}", hex::encode(with_context(&encode(&req), "review", &f).unwrap()));
    }
}
