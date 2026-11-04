//! The proxy listener. A client uses it as an HTTP proxy: plain HTTP
//! requests in absolute form, and CONNECT for TLS. The client
//! authenticates with Basic proxy authentication: the user is the
//! environment id, the password is its credential (see `credential`).

use std::sync::Arc;

use base64::Engine as _;
use base64::engine::general_purpose::STANDARD;
use hmac::{Hmac, Mac};
use http::{Request, Response, StatusCode};
use hyper::body::Incoming;
use hyper::service::service_fn;
use hyper_util::rt::TokioIo;
use sha2::Sha256;
use tokio::net::{TcpListener, TcpStream};

use crate::detect::{self, Protocol};
use crate::dns::{self, DNS_HOST};
use crate::flow::{Body, Peer, Proxy, Target, text_response};
use crate::magi::Access;

const DOMAIN: &[u8] = b"magi-egress-env:";

/// The proxy password of an environment: hex HMAC-SHA256 of its id.
pub fn credential(key: &[u8], env: &str) -> String {
    let mut m = Hmac::<Sha256>::new_from_slice(key).expect("HMAC key");
    m.update(DOMAIN);
    m.update(env.as_bytes());
    hex::encode(m.finalize().into_bytes())
}

fn verify(key: &[u8], env: &str, cred: &str) -> bool {
    let Ok(raw) = hex::decode(cred) else { return false };
    let mut m = Hmac::<Sha256>::new_from_slice(key).expect("HMAC key");
    m.update(DOMAIN);
    m.update(env.as_bytes());
    m.verify_slice(&raw).is_ok()
}

pub async fn serve(proxy: Arc<Proxy>, listener: TcpListener) -> anyhow::Result<()> {
    loop {
        let (stream, _) = listener.accept().await?;
        let p = proxy.clone();
        tokio::spawn(async move {
            let svc = service_fn(move |req| front(p.clone(), req));
            if let Err(e) = hyper::server::conn::http1::Builder::new().serve_connection(TokioIo::new(stream), svc).with_upgrades().await {
                tracing::debug!(error = %e, "proxy connection");
            }
        });
    }
}

async fn authenticate(p: &Proxy, req: &Request<Incoming>) -> Option<Peer> {
    let v = req.headers().get(http::header::PROXY_AUTHORIZATION)?.to_str().ok()?;
    let raw = STANDARD.decode(v.strip_prefix("Basic ")?.trim()).ok()?;
    let (env, cred) = std::str::from_utf8(&raw).ok()?.split_once(':')?;
    if env.is_empty() || !verify(&p.env_key, env, cred) {
        return None;
    }
    let mut peer = Peer { env: env.to_string(), owner: String::new() };
    if let Some(m) = &p.magi {
        match m.environment(env).await {
            Ok(info) => peer.owner = info.owner,
            Err(e) => tracing::warn!(environment = %env, error = %e, "Magi does not know the owner"),
        }
    }
    Some(peer)
}

fn split(authority: &str, default: u16) -> Option<(String, u16)> {
    let a: http::uri::Authority = authority.parse().ok()?;
    Some((a.host().trim_start_matches('[').trim_end_matches(']').to_ascii_lowercase(), a.port_u16().unwrap_or(default)))
}

async fn front(p: Arc<Proxy>, req: Request<Incoming>) -> Result<Response<Body>, std::convert::Infallible> {
    let Some(peer) = authenticate(&p, &req).await else {
        let mut r = text_response(StatusCode::PROXY_AUTHENTICATION_REQUIRED, "denied", "proxy credential required");
        r.headers_mut().insert(http::header::PROXY_AUTHENTICATE, http::HeaderValue::from_static("Basic realm=\"magi-egress\""));
        return Ok(r);
    };
    if req.method() == http::Method::CONNECT {
        return Ok(connect(p, peer, req).await);
    }
    let uri = req.uri().clone();
    let Some((host, port)) = uri.authority().and_then(|a| split(a.as_str(), 80)) else {
        return Ok(text_response(StatusCode::BAD_REQUEST, "blocked", "use the proxy with absolute URLs or CONNECT"));
    };
    if uri.scheme_str() != Some("http") {
        return Ok(text_response(StatusCode::BAD_REQUEST, "blocked", "only http:// URLs can go without CONNECT"));
    }
    Ok(p.handle(peer, Target { scheme: "http", host, port }, req).await)
}

async fn connect(p: Arc<Proxy>, peer: Peer, req: Request<Incoming>) -> Response<Body> {
    let Some((host, port)) = req.uri().authority().and_then(|a| split(a.as_str(), 443)) else {
        return text_response(StatusCode::BAD_REQUEST, "blocked", "CONNECT needs host:port");
    };
    if host == DNS_HOST && port == 53 {
        tokio::spawn(async move {
            if let Ok(up) = hyper::upgrade::on(req).await {
                dns::serve(p, peer, TokioIo::new(up)).await;
            }
        });
        return Response::new(crate::flow::full(vec![]));
    }
    let t = Target { scheme: "https", host: host.clone(), port };
    let access = match p.access(&peer, &t, "CONNECT", &format!("tcp://{host}:{port}")).await {
        Ok(a) => a,
        Err(s) => {
            p.record(&peer, &t, s.outcome, vec![s.reason.clone()]);
            return text_response(s.status, s.outcome, &s.reason);
        }
    };
    tokio::spawn(async move {
        let up = match hyper::upgrade::on(req).await {
            Ok(u) => u,
            Err(e) => return tracing::debug!(error = %e, "upgrade"),
        };
        let mut up = TokioIo::new(up);
        if access == Access::Layer4 {
            p.record(&peer, &t, "sent", vec!["layer 4 passthrough, not inspected".into()]);
            match TcpStream::connect((host.as_str(), port)).await {
                Ok(mut s) => {
                    let _ = tokio::io::copy_bidirectional(&mut up, &mut s).await;
                }
                Err(e) => tracing::warn!(destination = %t.destination(), error = %e, "passthrough connect"),
            }
            return;
        }
        let head = match detect::peek(&mut up).await {
            Ok(h) => h,
            Err(e) => return tracing::debug!(error = %e, "first bytes"),
        };
        let proto = detect::classify(&head);
        let io = detect::Prefixed::new(head, up);
        match proto {
            Protocol::Tls => {
                let cfg = match p.ca.server_config(&host) {
                    Ok(c) => c,
                    Err(e) => return p.record(&peer, &t, "failed", vec![format!("certificate: {e}")]),
                };
                match tokio_rustls::TlsAcceptor::from(cfg).accept(io).await {
                    Ok(s) => serve_tunnel(p, peer, t, TokioIo::new(s)).await,
                    Err(e) => p.record(&peer, &t, "failed", vec![format!("TLS from the client: {e}")]),
                }
            }
            Protocol::Http => serve_tunnel(p, peer, Target { scheme: "http", ..t }, TokioIo::new(io)).await,
            Protocol::Ssh if p.ssh_key.is_some() && p.policy.ssh_host_for(&peer.env, &host, port).is_some() => {
                crate::ssh::serve(p, peer, Target { scheme: "ssh", ..t }, io).await
            }
            other => {
                // The proxy cannot inspect this protocol. An admin can
                // permit the destination at layer 4.
                let reason = format!("{} to {} is not inspected; an admin can permit it at layer 4", other.name(), t.destination());
                let s = p.report_destination(&peer, &t, "CONNECT", &format!("tcp://{host}:{port}"), reason);
                p.record(&peer, &t, s.outcome, vec![s.reason]);
            }
        }
    });
    Response::new(crate::flow::full(Vec::<u8>::new()))
}

/// Serves the HTTP requests of an inspected tunnel.
async fn serve_tunnel<I>(p: Arc<Proxy>, peer: Peer, t: Target, io: I)
where
    I: hyper::rt::Read + hyper::rt::Write + Unpin + Send + 'static,
{
    let svc = service_fn(move |r: Request<Incoming>| {
        let (p, peer, t) = (p.clone(), peer.clone(), t.clone());
        async move { Ok::<_, std::convert::Infallible>(p.handle(peer, t, r).await) }
    });
    if let Err(e) = hyper::server::conn::http1::Builder::new().serve_connection(io, svc).await {
        tracing::debug!(error = %e, "tunnel connection");
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn credentials() {
        let c = credential(b"key-1", "env_a");
        assert_eq!(c.len(), 64);
        assert!(verify(b"key-1", "env_a", &c));
        assert!(!verify(b"key-1", "env_b", &c));
        assert!(!verify(b"key-2", "env_a", &c));
        assert!(!verify(b"key-1", "env_a", "zz"));
    }

    /// The same vector is in primitives/execution/backend/egress_test.go.
    #[test]
    fn credential_vector() {
        assert_eq!(credential(b"0123456789abcdef0123456789abcdef", "env_a"), "06863b8c6d38ee30f3b0490b804c5eee1a8f7280cd7852d35b347ec84cf94a2c");
    }

    #[test]
    fn authorities() {
        assert_eq!(split("API.example.local:8443", 443), Some(("api.example.local".into(), 8443)));
        assert_eq!(split("example.local", 443), Some(("example.local".into(), 443)));
        assert_eq!(split("[::1]:80", 443), Some(("::1".into(), 80)));
    }
}
