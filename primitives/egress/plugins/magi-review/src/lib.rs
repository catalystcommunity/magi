//! Asks Magi to evaluate the flow. Magi answers "pass", "review", or
//! "block". When Magi cannot answer, the flow stops for review: the proxy
//! blocks it, and an admin reviews it later.

wit_bindgen::generate!({ path: "../../../../contracts/egress.wit", world: "filter" });

use magi::egress::host;
use serde::Deserialize;

#[derive(Deserialize)]
struct Verdict {
    verdict: String,
    reason: String,
    by: String,
}

struct MagiReview;

impl Guest for MagiReview {
    fn check(_f: Flow) -> Vec<Action> {
        let mut req = Vec::new();
        ciborium::into_writer(&ciborium::Value::Map(vec![]), &mut req).unwrap();
        let v: Verdict = match host::magi("evaluate", &req).map(|b| ciborium::from_reader(&b[..])) {
            Ok(Ok(v)) => v,
            Ok(Err(e)) => return vec![Action::Review(format!("the answer of Magi cannot be read: {e}"))],
            Err(e) => return vec![Action::Review(format!("Magi could not evaluate: {e}"))],
        };
        let why = format!("{}: {}", v.by, v.reason);
        match v.verdict.as_str() {
            "pass" => vec![Action::Pass],
            "block" => vec![Action::Block(why)],
            "review" => vec![Action::Review(why)],
            other => vec![Action::Review(format!("unknown verdict {other:?} from {why}"))],
        }
    }
}

export!(MagiReview);
