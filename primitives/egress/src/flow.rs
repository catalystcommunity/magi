//! The work on one flow (docs/egress-proxy.md, "Order of work"):
//! destination check, decode, built-in secret scan, plugins, review, secret
//! placeholders, send, and record. The steps that keep secrets in (the
//! destination check, the decode depth, the scan, and the placeholder
//! check) are here in the host, so a plugin cannot turn them off.

use std::sync::Arc;
use std::time::Instant;

use bytes::Bytes;
use http::{HeaderMap, HeaderName, HeaderValue, Request, Response, StatusCode};
use http_body_util::combinators::BoxBody;
use http_body_util::{BodyExt as _, Full, Limited};
use rand::Rng as _;

use crate::api::FlowRecord;
use crate::ca::Ca;
use crate::magi::{Access, Magi, spawn_report};
use crate::records::Records;
use crate::net::Client;
use crate::plugins::{Action, Direction, Flow, Header, Plugins, SecretUse};
use crate::policy::Policy;
use crate::scan::{self, PLACEHOLDER, Secrets};

pub type Body = BoxBody<Bytes, hyper::Error>;

/// Headers that belong to one connection. The proxy does not pass them on.
const HOP: &[&str] = &[
    "connection", "keep-alive", "proxy-authorization", "proxy-authenticate", "proxy-connection", "te", "trailer",
    "transfer-encoding", "upgrade", "content-length", "content-encoding", "host",
];

pub struct Proxy {
    pub policy: Policy,
    pub secrets: Secrets,
    pub plugins: Plugins,
    pub magi: Option<Arc<Magi>>,
    pub records: Arc<Records>,
    pub ca: Ca,
    pub upstream: Client,
    pub env_key: Vec<u8>,
    /// The SSH host key of the proxy (ssh.rs). None turns SSH off.
    pub ssh_key: Option<russh::keys::PrivateKey>,
}

/// The environment that sent a flow, from its proxy credential.
#[derive(Debug, Clone, Default)]
pub struct Peer {
    pub env: String,
    pub owner: String,
}

/// Where the flow goes.
#[derive(Debug, Clone)]
pub struct Target {
    pub scheme: &'static str,
    pub host: String,
    pub port: u16,
}

impl Target {
    pub fn destination(&self) -> String {
        format!("{}:{}", self.host, self.port)
    }
    fn base(&self) -> String {
        let default = (self.scheme == "https" && self.port == 443) || (self.scheme == "http" && self.port == 80);
        if default { format!("{}://{}", self.scheme, self.host) } else { format!("{}://{}:{}", self.scheme, self.host, self.port) }
    }
}

/// The result of one step: go on, or answer the client now.
type Step<T> = Result<T, Stop>;

#[derive(Debug)]
pub struct Stop {
    pub status: StatusCode,
    pub reason: String,
    pub outcome: &'static str,
}

fn block(reason: impl Into<String>) -> Stop {
    Stop { status: StatusCode::FORBIDDEN, reason: reason.into(), outcome: "blocked" }
}

/// Blocked now; an admin reviews the policy later.
fn review(reason: impl Into<String>) -> Stop {
    Stop {
        status: StatusCode::FORBIDDEN,
        reason: format!("{} (blocked for review: an admin reviews the policy later; the request was not sent)", reason.into()),
        outcome: "review",
    }
}

pub fn new_id(prefix: &str) -> String {
    format!("{prefix}{:016x}", rand::rng().random::<u64>())
}

pub fn text_response(status: StatusCode, outcome: &str, text: &str) -> Response<Body> {
    let mut r = Response::new(full(format!("magi egress: {text}\n").into_bytes()));
    *r.status_mut() = status;
    r.headers_mut().insert("x-magi-egress", HeaderValue::from_str(outcome).unwrap_or(HeaderValue::from_static("blocked")));
    r.headers_mut().insert(http::header::CONTENT_TYPE, HeaderValue::from_static("text/plain; charset=utf-8"));
    r
}

pub fn full(b: Vec<u8>) -> Body {
    Full::new(Bytes::from(b)).map_err(|never| match never {}).boxed()
}

impl Proxy {
    /// Checks the destination of an HTTP request. See `access`.
    pub async fn permitted(&self, peer: &Peer, t: &Target, method: &str, url: &str) -> Result<(), Stop> {
        self.access(peer, t, method, url).await.map(|_| ())
    }

    /// What the destination may do. The policy permits it (`destinations`
    /// inspected, `passthrough` layer 4), or a decided review in Magi
    /// permits it (only on the policy's review_ports; empty means all).
    /// Else the flow stops for review, and Magi gets one report for each
    /// environment and destination in a minute.
    pub async fn access(&self, peer: &Peer, t: &Target, method: &str, url: &str) -> Result<Access, Stop> {
        if self.policy.destination(&peer.env, &t.host, t.port) || self.policy.ssh_host_for(&peer.env, &t.host, t.port).is_some() {
            return Ok(Access::Inspect);
        }
        if self.policy.passthrough(&peer.env, &t.host, t.port) {
            return Ok(Access::Layer4);
        }
        let reason = format!("destination {} is not in the policy", t.destination());
        let Some(m) = &self.magi else { return Err(block(reason)) };
        let rp = &self.policy.review_ports;
        if rp.is_empty() || rp.contains(&t.port) {
            let a = m.destination(&peer.env, &t.destination()).await;
            if a != Access::No {
                return Ok(a);
            }
        }
        Err(self.report_destination(peer, t, method, url, reason))
    }

    /// Reports a destination for review (once a minute for each
    /// environment and destination), and returns the stop.
    pub fn report_destination(&self, peer: &Peer, t: &Target, method: &str, url: &str, reason: String) -> Stop {
        let dest = t.destination();
        if let Some(m) = &self.magi
            && m.first_report(&peer.env, &dest)
        {
            let f = Flow {
                id: new_id("flw_"),
                environment: peer.env.clone(),
                owner: peer.owner.clone(),
                direction: Direction::Out,
                destination: dest,
                method: method.into(),
                url: url.into(),
                status: 0,
                headers: vec![],
                body: vec![],
            };
            spawn_report(m, "destination", f, reason.clone(), "policy".into());
        }
        if self.magi.is_some() { review(reason) } else { block(reason) }
    }

    /// Stops a flow that a plugin wants a person to review, and reports it
    /// to Magi. The flow is after the scan: it has placeholders, not
    /// secrets.
    fn review(&self, f: &Flow, reviews: Vec<(String, String)>) -> Stop {
        let reason = reviews.iter().map(|(p, r)| format!("plugin {p}: {r}")).collect::<Vec<_>>().join("; ");
        let by = reviews.iter().map(|(p, _)| p.as_str()).collect::<Vec<_>>().join(", ");
        match &self.magi {
            Some(m) => spawn_report(m, "request", f.clone(), reason.clone(), by),
            None => tracing::warn!(flow = %f.id, "no Magi: the review is only in the log"),
        }
        review(reason)
    }

    pub fn record(&self, peer: &Peer, t: &Target, outcome: &str, reasons: Vec<String>) {
        self.records.add(FlowRecord {
            id: new_id("flw_"),
            environment: peer.env.clone(),
            owner: peer.owner.clone(),
            destination: t.destination(),
            method: None,
            url: None,
            status: None,
            outcome: outcome.into(),
            reasons,
            plugins: vec![],
            secrets_used: vec![],
            redacted: 0,
            created_ms: crate::now_ms(),
            duration_ms: 0,
        });
    }

    /// One request and its response.
    pub async fn handle<B>(self: Arc<Self>, peer: Peer, t: Target, req: Request<B>) -> Response<Body>
    where
        B: hyper::body::Body<Data = Bytes> + Send,
        B::Error: Into<Box<dyn std::error::Error + Send + Sync>>,
    {
        let start = Instant::now();
        let mut rec = FlowRecord {
            id: new_id("flw_"),
            environment: peer.env.clone(),
            owner: peer.owner.clone(),
            destination: t.destination(),
            method: Some(req.method().to_string()),
            url: None,
            status: None,
            outcome: "sent".into(),
            reasons: vec![],
            plugins: vec![],
            secrets_used: vec![],
            redacted: 0,
            created_ms: crate::now_ms(),
            duration_ms: 0,
        };
        let res = match self.exchange(&mut rec, &peer, &t, req).await {
            Ok(r) => r,
            Err(s) => {
                rec.outcome = s.outcome.into();
                rec.reasons.push(s.reason.clone());
                text_response(s.status, s.outcome, &s.reason)
            }
        };
        rec.status = Some(res.status().as_u16() as u64);
        rec.duration_ms = start.elapsed().as_millis() as u64;
        self.records.add(rec);
        res
    }

    async fn exchange<B>(&self, rec: &mut FlowRecord, peer: &Peer, t: &Target, req: Request<B>) -> Step<Response<Body>>
    where
        B: hyper::body::Body<Data = Bytes> + Send,
        B::Error: Into<Box<dyn std::error::Error + Send + Sync>>,
    {
        // 1. Destination.
        let path = req.uri().path_and_query().map_or("/", |p| p.as_str());
        let url = format!("{}{path}", t.base());
        self.permitted(peer, t, req.method().as_str(), &url).await?;
        if let Some(h) = req.headers().get(http::header::HOST).and_then(|v| v.to_str().ok()) {
            let name = h.rsplit_once(':').filter(|(_, p)| p.parse::<u16>().is_ok()).map_or(h, |(n, _)| n);
            if !name.eq_ignore_ascii_case(&t.host) {
                return Err(block(format!("Host {h} is not the destination {}", t.host)));
            }
        }
        if req.method() == http::Method::CONNECT || req.headers().contains_key(http::header::UPGRADE) {
            return Err(block("protocol upgrades and nested tunnels are not permitted"));
        }
        let (parts, body) = req.into_parts();
        rec.url = Some(url.clone());

        // 2. Decode.
        let raw = Limited::new(body, self.policy.max_body)
            .collect()
            .await
            .map_err(|e| block(format!("request body: {e} (the limit is {} bytes)", self.policy.max_body)))?
            .to_bytes();
        let body = decode_body(&parts.headers, &raw, self.policy.max_body)?;

        // 3. Built-in secret scan.
        let depth = self.policy.depth_for(&t.host);
        let mut flow = Flow {
            id: rec.id.clone(),
            environment: peer.env.clone(),
            owner: peer.owner.clone(),
            direction: Direction::Out,
            destination: t.destination(),
            method: parts.method.to_string(),
            url: url.clone(),
            status: 0,
            headers: headers_of(&parts.headers),
            body,
        };
        self.clean(rec, &mut flow, Some(depth))?;

        // 4. Plugins.
        let (reviews, uses) = self.run_plugins(rec, &mut flow).await?;
        self.clean(rec, &mut flow, Some(depth))?;

        // 5. Review: block now, and Magi keeps the flow for an admin.
        if !reviews.is_empty() {
            return Err(self.review(&flow, reviews));
        }

        // 6. Secrets in place of placeholders, then no placeholder must stay.
        for (plugin, u) in uses {
            self.use_secret(rec, &mut flow, &t.host, &plugin, &u)?;
        }
        // The body of a model call of a Magi service (svc_*) is data: the
        // conversation and tool output, which can quote placeholders (for
        // example the output of these tests). Its key is in a header.
        let left = placeholders(&flow, !flow.environment.starts_with("svc_"));
        if !left.is_empty() {
            return Err(block(format!("no secret for placeholder {} at {}", left.join(", "), t.host)));
        }

        // 7. Send.
        let mut up = Request::builder().method(parts.method.clone()).uri(&flow.url);
        for h in &flow.headers {
            if !HOP.contains(&h.name.to_ascii_lowercase().as_str()) && !h.name.eq_ignore_ascii_case("accept-encoding") {
                up = up.header(&h.name, &h.value);
            }
        }
        // Ask for an identity body, so the response can be scanned.
        up = up.header(http::header::ACCEPT_ENCODING, "identity");
        let up = up.body(Full::new(Bytes::from(std::mem::take(&mut flow.body)))).map_err(|e| block(format!("request: {e}")))?;
        let res = self
            .upstream
            .request(up)
            .await
            .map_err(|e| Stop { status: StatusCode::BAD_GATEWAY, reason: format!("upstream {}: {}", t.destination(), err_chain(&e)), outcome: "failed" })?;
        self.response(rec, peer, t, &parts.method, &url, res).await
    }

    async fn response(&self, rec: &mut FlowRecord, peer: &Peer, t: &Target, method: &http::Method, url: &str, res: Response<hyper::body::Incoming>) -> Step<Response<Body>> {
        let (parts, body) = res.into_parts();
        let len = parts.headers.get(http::header::CONTENT_LENGTH).and_then(|v| v.to_str().ok()).and_then(|v| v.parse::<usize>().ok());
        if len.is_some_and(|n| n > self.policy.max_body) {
            if self.policy.streams(&t.host) {
                rec.reasons.push("large response sent without inspection".into());
                let mut out = Response::from_parts(parts, body.boxed());
                strip(out.headers_mut(), false);
                return Ok(out);
            }
            return Err(Stop { status: StatusCode::BAD_GATEWAY, reason: format!("response is larger than {} bytes", self.policy.max_body), outcome: "blocked" });
        }
        let raw = Limited::new(body, self.policy.max_body)
            .collect()
            .await
            .map_err(|e| Stop { status: StatusCode::BAD_GATEWAY, reason: format!("response body: {e}"), outcome: "blocked" })?
            .to_bytes();
        let body = decode_body(&parts.headers, &raw, self.policy.max_body)?;
        let mut flow = Flow {
            id: rec.id.clone(),
            environment: peer.env.clone(),
            owner: peer.owner.clone(),
            direction: Direction::In,
            destination: t.destination(),
            method: method.to_string(),
            url: url.to_string(),
            status: parts.status.as_u16(),
            headers: headers_of(&parts.headers),
            body,
        };
        // A response can carry encoded data by design: no depth limit.
        self.clean(rec, &mut flow, None)?;
        let (reviews, uses) = self.run_plugins(rec, &mut flow).await?;
        if !uses.is_empty() {
            return Err(block("a plugin tried to put a secret in a response"));
        }
        self.clean(rec, &mut flow, None)?;
        if !reviews.is_empty() {
            return Err(self.review(&flow, reviews));
        }
        let mut out = Response::new(full(flow.body));
        *out.status_mut() = parts.status;
        for h in &flow.headers {
            if let (Ok(n), Ok(v)) = (HeaderName::from_bytes(h.name.as_bytes()), HeaderValue::from_str(&h.value)) {
                out.headers_mut().append(n, v);
            }
        }
        strip(out.headers_mut(), true);
        Ok(out)
    }

    /// Steps 3 to 5 for a flow that is not an HTTP request (an SSH
    /// command): the scan, the plugins, and the review. A flow of this kind
    /// cannot take secrets, and must have no placeholder.
    pub async fn inspect_out(&self, rec: &mut FlowRecord, f: &mut Flow, host: &str) -> Step<()> {
        let depth = self.policy.depth_for(host);
        self.clean(rec, f, Some(depth))?;
        let (reviews, uses) = self.run_plugins(rec, f).await?;
        self.clean(rec, f, Some(depth))?;
        if !reviews.is_empty() {
            return Err(self.review(f, reviews));
        }
        if !uses.is_empty() {
            return Err(block("a plugin tried to put a secret in an SSH command"));
        }
        let left = placeholders(f, true);
        if !left.is_empty() {
            return Err(block(format!("placeholder {} in an SSH command", left.join(", "))));
        }
        Ok(())
    }

    /// The built-in scan of the URL, the headers, and the body. With
    /// `depth`, data that is encoded more times stops the flow.
    fn clean(&self, rec: &mut FlowRecord, f: &mut Flow, depth: Option<u32>) -> Step<()> {
        let limit = depth.unwrap_or(16);
        let mut check = |data: &[u8], what: &str| -> Step<Vec<u8>> {
            let s = scan::scan(data, &self.secrets, limit);
            if !s.hidden.is_empty() {
                return Err(block(format!("{what} has secret {} in an encoded form", s.hidden.join(", "))));
            }
            if let Some(d) = depth
                && s.depth > d
            {
                return Err(block(format!("{what} is encoded {} times; the limit is {d}", s.depth)));
            }
            rec.redacted += s.redacted.len() as u64;
            for id in s.redacted {
                rec.reasons.push(format!("removed secret {id} from {what}"));
            }
            Ok(s.data)
        };
        if f.direction == Direction::Out {
            f.url = String::from_utf8_lossy(&check(f.url.as_bytes(), "the URL")?).into_owned();
        }
        for h in f.headers.iter_mut() {
            h.value = String::from_utf8_lossy(&check(h.value.as_bytes(), &format!("header {}", h.name))?).into_owned();
        }
        f.body = check(&f.body, "the body")?;
        Ok(())
    }

    async fn run_plugins(&self, rec: &mut FlowRecord, f: &mut Flow) -> Step<(Vec<(String, String)>, Vec<(String, SecretUse)>)> {
        let mut reviews = Vec::new();
        let mut uses = Vec::new();
        let list: Vec<_> = self.plugins.for_flow(f).collect();
        for p in list {
            let name = p.grant.name.clone();
            if !rec.plugins.contains(&name) {
                rec.plugins.push(name.clone());
            }
            let actions = self.plugins.run(p, f).await.map_err(|e| block(e.to_string()))?;
            for a in actions {
                match a {
                    Action::Pass => {}
                    Action::Block(r) => return Err(block(format!("plugin {name}: {r}"))),
                    Action::Review(r) => reviews.push((name.clone(), r)),
                    Action::SetBody(b) => {
                        rec.reasons.push(format!("plugin {name} changed the body"));
                        f.body = b;
                    }
                    Action::SetHeader(h) => {
                        if HOP.contains(&h.name.to_ascii_lowercase().as_str()) || HeaderName::from_bytes(h.name.as_bytes()).is_err() {
                            return Err(block(format!("plugin {name} cannot set header {:?}", h.name)));
                        }
                        f.headers.retain(|x| !x.name.eq_ignore_ascii_case(&h.name));
                        if !h.value.is_empty() {
                            f.headers.push(h.clone());
                        }
                        rec.reasons.push(format!("plugin {name} set header {}", h.name));
                    }
                    Action::UseSecret(u) => uses.push((name.clone(), u)),
                }
            }
        }
        Ok((reviews, uses))
    }

    fn use_secret(&self, rec: &mut FlowRecord, f: &mut Flow, host: &str, plugin: &str, u: &SecretUse) -> Step<()> {
        if !PLACEHOLDER.find(u.placeholder.as_bytes()).is_some_and(|m| m.as_bytes() == u.placeholder.as_bytes()) {
            return Err(block(format!("plugin {plugin}: {:?} is not a placeholder", u.placeholder)));
        }
        let Some(value) = self.secrets.get(&u.secret) else {
            return Err(block(format!("plugin {plugin}: no secret {}", u.secret)));
        };
        if !self.policy.secret_permitted(&u.secret, host) {
            return Err(block(format!("secret {} is not permitted for {host}", u.secret)));
        }
        let value = String::from_utf8_lossy(value).into_owned();
        let (header, url, body) = match u.place.as_str() {
            "any" => (None, true, true),
            "url" => (Some(""), true, false),
            "body" => (Some(""), false, true),
            p => match p.strip_prefix("header:") {
                Some(n) if !n.is_empty() => (Some(n), false, false),
                _ => return Err(block(format!("plugin {plugin}: place {p:?} is not valid"))),
            },
        };
        let mut n = 0;
        for h in f.headers.iter_mut() {
            if header.is_none() || header.is_some_and(|x| x.eq_ignore_ascii_case(&h.name)) {
                n += h.value.matches(&u.placeholder).count();
                h.value = h.value.replace(&u.placeholder, &value);
            }
        }
        if url {
            n += f.url.matches(&u.placeholder).count();
            f.url = f.url.replace(&u.placeholder, &value);
        }
        if body {
            let re = regex::bytes::Regex::new(&regex::escape(&u.placeholder)).expect("escaped");
            n += re.find_iter(&f.body).count();
            f.body = re.replace_all(&f.body, value.as_bytes()).into_owned();
        }
        if n > 0 && !rec.secrets_used.contains(&u.secret) {
            rec.secrets_used.push(u.secret.clone());
        }
        Ok(())
    }
}

/// The placeholders in the URL, the headers, and (with body) the body.
fn placeholders(f: &Flow, body: bool) -> Vec<String> {
    let mut v: Vec<String> = Vec::new();
    let mut add = |b: &[u8]| {
        for m in PLACEHOLDER.find_iter(b) {
            v.push(String::from_utf8_lossy(m.as_bytes()).into_owned());
        }
    };
    add(f.url.as_bytes());
    for h in &f.headers {
        add(h.value.as_bytes());
    }
    if body {
        add(&f.body);
    }
    v.sort();
    v.dedup();
    v
}

fn headers_of(h: &HeaderMap) -> Vec<Header> {
    h.iter()
        .filter(|(n, _)| !matches!(n.as_str(), "proxy-authorization" | "proxy-connection" | "content-length" | "content-encoding" | "transfer-encoding"))
        .map(|(n, v)| Header { name: n.as_str().to_string(), value: String::from_utf8_lossy(v.as_bytes()).into_owned() })
        .collect()
}

fn strip(h: &mut HeaderMap, decoded: bool) {
    for n in ["connection", "keep-alive", "proxy-authenticate", "proxy-connection", "te", "trailer", "transfer-encoding", "upgrade"] {
        h.remove(n);
    }
    if decoded {
        h.remove(http::header::CONTENT_ENCODING);
        h.remove(http::header::CONTENT_LENGTH);
    }
}

/// Removes the content encoding. An encoding that the proxy cannot read
/// stops the flow.
fn decode_body(h: &HeaderMap, raw: &[u8], max: usize) -> Step<Vec<u8>> {
    use std::io::Read as _;
    let enc = h.get(http::header::CONTENT_ENCODING).and_then(|v| v.to_str().ok()).unwrap_or("").trim().to_ascii_lowercase();
    match enc.as_str() {
        "" | "identity" => Ok(raw.to_vec()),
        "gzip" | "x-gzip" => scan::gunzip(raw, max).ok_or_else(|| block("the gzip body cannot be decoded")),
        "deflate" => {
            let mut out = Vec::new();
            flate2::read::ZlibDecoder::new(raw).take(max as u64 + 1).read_to_end(&mut out).map_err(|_| block("the deflate body cannot be decoded"))?;
            if out.len() > max {
                return Err(block("the decoded body is too large"));
            }
            Ok(out)
        }
        e => Err(block(format!("content encoding {e:?} is not supported"))),
    }
}

fn err_chain(e: &dyn std::error::Error) -> String {
    let mut s = e.to_string();
    let mut cur = e.source();
    while let Some(c) = cur {
        s.push_str(": ");
        s.push_str(&c.to_string());
        cur = c.source();
    }
    s
}
