//! Verification of lib/authn bearer tokens:
//! `magi1.<b64url JSON payload>.<b64url Ed25519 signature over "magi1.<payload>">`.

use std::collections::HashMap;
use std::path::Path;

use base64::Engine;
use base64::engine::general_purpose::{STANDARD, URL_SAFE_NO_PAD};
use ed25519_dalek::{Signature, Verifier as _, VerifyingKey};
use serde::Deserialize;

const PREFIX: &str = "magi1.";

/// An authenticated caller. Field names follow the lib/authn JSON payload.
#[derive(Debug, Clone, Default, PartialEq, Deserialize)]
#[serde(default)]
pub struct Principal {
    pub sub: String,
    pub kind: String,
    pub scopes: Option<Vec<String>>,
    pub iss: String,
    pub kid: String,
    pub iat: i64,
    pub exp: i64,
}

impl Principal {
    /// Same rule as lib/authn `Has`: "*" holds every scope, "name:*" holds
    /// every scope that starts with "name:".
    pub fn has(&self, scope: &str) -> bool {
        self.scopes.iter().flatten().any(|s| {
            s == "*"
                || s == scope
                || (s.ends_with(":*") && scope.starts_with(s.trim_end_matches('*')))
        })
    }
}

/// Error text matches lib/authn.
#[derive(Debug, Clone, PartialEq)]
pub enum AuthError {
    Invalid,
    Expired,
}

impl std::fmt::Display for AuthError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(match self {
            AuthError::Invalid => "invalid token",
            AuthError::Expired => "token expired",
        })
    }
}

/// On-disk issuer key. Only the public key is read.
#[derive(Debug, Deserialize)]
pub struct KeyFile {
    pub issuer: String,
    pub kid: String,
    pub public_key: String,
}

#[derive(Debug, Default, Clone)]
pub struct Verifier {
    pub issuer: String,
    pub keys: HashMap<String, VerifyingKey>,
}

impl Verifier {
    /// Reads one or more key files. All files must name the same issuer.
    pub fn load<P: AsRef<Path>>(paths: &[P]) -> anyhow::Result<Self> {
        let mut v = Verifier::default();
        for p in paths {
            let p = p.as_ref();
            let k: KeyFile = serde_json::from_slice(&std::fs::read(p)?)
                .map_err(|e| anyhow::anyhow!("key file {}: {e}", p.display()))?;
            v.add(k).map_err(|e| anyhow::anyhow!("key file {}: {e}", p.display()))?;
        }
        Ok(v)
    }

    pub fn add(&mut self, k: KeyFile) -> anyhow::Result<()> {
        let raw = STANDARD.decode(k.public_key.trim())?;
        let bytes: [u8; 32] = raw.try_into().map_err(|_| anyhow::anyhow!("bad public key"))?;
        if !self.issuer.is_empty() && self.issuer != k.issuer {
            anyhow::bail!("issuer mismatch");
        }
        self.issuer = k.issuer;
        self.keys.insert(k.kid, VerifyingKey::from_bytes(&bytes)?);
        Ok(())
    }

    /// Returns the principal of a valid token that has not expired at `now` (Unix seconds).
    pub fn verify(&self, token: &str, now: i64) -> Result<Principal, AuthError> {
        if !token.starts_with(PREFIX) || token.len() > 8192 {
            return Err(AuthError::Invalid);
        }
        let dot = token.rfind('.').ok_or(AuthError::Invalid)?;
        if dot <= PREFIX.len() {
            return Err(AuthError::Invalid);
        }
        let (signed, sig_text) = (&token[..dot], &token[dot + 1..]);
        let sig = URL_SAFE_NO_PAD.decode(sig_text).map_err(|_| AuthError::Invalid)?;
        let sig = Signature::from_slice(&sig).map_err(|_| AuthError::Invalid)?;
        let body = URL_SAFE_NO_PAD
            .decode(&signed[PREFIX.len()..])
            .map_err(|_| AuthError::Invalid)?;
        let p: Principal = serde_json::from_slice(&body).map_err(|_| AuthError::Invalid)?;
        let key = self.keys.get(&p.kid).ok_or(AuthError::Invalid)?;
        if p.iss != self.issuer || key.verify(signed.as_bytes(), &sig).is_err() {
            return Err(AuthError::Invalid);
        }
        if now >= p.exp {
            return Err(AuthError::Expired);
        }
        Ok(p)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn vector() -> (Verifier, serde_json::Value) {
        let v: serde_json::Value =
            serde_json::from_str(include_str!("../testdata/authn-vector.json")).unwrap();
        let mut ver = Verifier::default();
        ver.add(serde_json::from_value(v["key"].clone()).unwrap()).unwrap();
        (ver, v)
    }

    #[test]
    fn verifies_go_tokens() {
        let (ver, v) = vector();
        let now = v["now"].as_i64().unwrap();
        let tok = |n: &str| v[n].as_str().unwrap().to_string();
        let op = ver.verify(&tok("operator"), now).unwrap();
        assert_eq!(op.sub, "service:magi");
        assert_eq!(op.kind, "service");
        assert_eq!(op.iss, "magi.example.local");
        assert!(op.has("release:operate") && !op.has("release:admin"));
        let rd = ver.verify(&tok("reader"), now).unwrap();
        assert!(rd.has("release:read") && !rd.has("release:operate"));
        let all = ver.verify(&tok("wildcard"), now).unwrap();
        assert!(all.has("release:admin"));
        let none = ver.verify(&tok("noscope"), now).unwrap();
        assert!(none.scopes.is_none() && !none.has("release:read"));
        assert_eq!(ver.verify(&tok("expired"), now), Err(AuthError::Expired));
    }

    #[test]
    fn rejects_bad_tokens() {
        let (ver, v) = vector();
        let now = v["now"].as_i64().unwrap();
        let good = v["operator"].as_str().unwrap();
        // Changed payload, changed signature, wrong prefix, wrong issuer key.
        let mut bad = good.replacen("eyJzdWIiOiJzZXJ2aWNl", "eyJzdWIiOiJzZXJ2aWNm", 1);
        assert_eq!(ver.verify(&bad, now), Err(AuthError::Invalid));
        bad = format!("{}A", &good[..good.len() - 1]);
        assert_eq!(ver.verify(&bad, now), Err(AuthError::Invalid));
        assert_eq!(ver.verify(&good.replacen("magi1.", "magi2.", 1), now), Err(AuthError::Invalid));
        assert_eq!(ver.verify("magi1..x", now), Err(AuthError::Invalid));
        let mut other = ver.clone();
        other.issuer = "other.example.local".into();
        assert_eq!(other.verify(good, now), Err(AuthError::Invalid));
        other = ver.clone();
        other.keys = HashMap::new();
        assert_eq!(other.verify(good, now), Err(AuthError::Invalid));
    }

    #[test]
    fn scope_rules() {
        let p = |s: &[&str]| Principal { scopes: Some(s.iter().map(|x| x.to_string()).collect()), ..Default::default() };
        assert!(p(&["release:*"]).has("release:operate"));
        assert!(!p(&["releases:*"]).has("release:operate"));
        assert!(!p(&["release:read"]).has("release:operate"));
        assert!(p(&["*"]).has("anything"));
    }
}
