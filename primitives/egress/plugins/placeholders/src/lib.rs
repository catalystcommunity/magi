//! Puts secrets in place of placeholders. The environment has only
//! placeholders ("magi-secret-<name>"). This plugin asks Magi for the
//! placeholder rules of the owner of the flow, and returns a use-secret
//! action for each placeholder whose rule matches the host and the path.
//! The proxy puts the value in; this plugin never sees it.

wit_bindgen::generate!({ path: "../../../../contracts/egress.wit", world: "filter" });

use magi::egress::host;
use magi::egress::types::{Direction, SecretUse};
use serde::Deserialize;

#[derive(Deserialize)]
struct Rules {
    rules: Vec<Rule>,
}

#[derive(Deserialize)]
struct Rule {
    placeholder: String,
    secret: String,
    hosts: Vec<String>,
    path: Option<String>,
    place: String,
}

struct Placeholders;

fn host_match(pattern: &str, host: &str) -> bool {
    let (p, h) = (pattern.to_ascii_lowercase(), host.to_ascii_lowercase());
    match p.strip_prefix("*.") {
        _ if p == "*" => true,
        Some(base) => h.len() > base.len() + 1 && h.ends_with(&format!(".{base}")),
        None => p == h,
    }
}

/// The path of a URL such as "https://host/a/b?q".
fn path(url: &str) -> &str {
    let rest = url.split_once("://").map_or(url, |(_, r)| r);
    rest.find('/').map_or("/", |i| &rest[i..])
}

impl Guest for Placeholders {
    fn check(f: Flow) -> Vec<Action> {
        if f.direction != Direction::Out {
            return vec![];
        }
        let re = regex::Regex::new(r"magi-secret-[A-Za-z0-9_.-]+").unwrap();
        let body = String::from_utf8_lossy(&f.body);
        let mut found: Vec<String> = Vec::new();
        for text in std::iter::once(f.url.as_str()).chain(f.headers.iter().map(|h| h.value.as_str())).chain(std::iter::once(&*body)) {
            found.extend(re.find_iter(text).map(|m| m.as_str().to_string()));
        }
        found.sort();
        found.dedup();
        if found.is_empty() {
            return vec![];
        }
        let mut req = Vec::new();
        ciborium::into_writer(&ciborium::Value::Map(vec![]), &mut req).unwrap();
        let rules: Rules = match host::magi("secret-rules", &req) {
            Ok(b) => match ciborium::from_reader(&b[..]) {
                Ok(r) => r,
                Err(e) => return vec![Action::Block(format!("the rules from Magi cannot be read: {e}"))],
            },
            Err(e) => return vec![Action::Block(format!("Magi did not give the placeholder rules: {e}"))],
        };
        let host = f.destination.rsplit_once(':').map_or(f.destination.as_str(), |(h, _)| h);
        let p = path(&f.url);
        let mut out = Vec::new();
        for ph in found {
            let rule = rules.rules.iter().find(|r| {
                r.placeholder == ph
                    && r.hosts.iter().any(|h| host_match(h, host))
                    && r.path.as_deref().is_none_or(|x| regex::Regex::new(x).is_ok_and(|re| re.is_match(p)))
            });
            match rule {
                Some(r) => out.push(Action::UseSecret(SecretUse { placeholder: ph, secret: r.secret.clone(), place: r.place.clone() })),
                None => host::log(&format!("no rule for {ph} at {host}{p}")),
            }
        }
        out
    }
}

export!(Placeholders);
