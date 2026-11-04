//! The flow records. They are in memory, and they also go to the log.

use std::collections::VecDeque;
use std::sync::Mutex;

use crate::api::FlowRecord;

pub struct Records {
    max: usize,
    list: Mutex<VecDeque<FlowRecord>>,
}

impl Records {
    pub fn new(max: usize) -> Self {
        Records { max, list: Mutex::new(VecDeque::new()) }
    }

    pub fn add(&self, r: FlowRecord) {
        tracing::info!(
            flow = %r.id, environment = %r.environment, owner = %r.owner, destination = %r.destination,
            method = r.method.as_deref().unwrap_or(""), url = r.url.as_deref().unwrap_or(""), status = r.status.unwrap_or(0),
            outcome = %r.outcome, reasons = ?r.reasons, plugins = ?r.plugins, secrets_used = ?r.secrets_used,
            redacted = r.redacted, ms = r.duration_ms, "flow"
        );
        let mut l = self.list.lock().unwrap();
        l.push_back(r);
        while l.len() > self.max {
            l.pop_front();
        }
    }

    /// Newest first.
    pub fn list(&self, env: Option<&str>, owner: Option<&str>, limit: usize) -> Vec<FlowRecord> {
        self.list
            .lock()
            .unwrap()
            .iter()
            .rev()
            .filter(|r| env.is_none_or(|e| r.environment == e) && owner.is_none_or(|o| r.owner == o))
            .take(limit)
            .cloned()
            .collect()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn rec(env: &str, owner: &str) -> FlowRecord {
        FlowRecord {
            id: "f".into(),
            environment: env.into(),
            owner: owner.into(),
            destination: "a:443".into(),
            method: None,
            url: None,
            status: None,
            outcome: "sent".into(),
            reasons: vec![],
            plugins: vec![],
            secrets_used: vec![],
            redacted: 0,
            created_ms: 1,
            duration_ms: 0,
        }
    }

    #[test]
    fn keeps_the_newest() {
        let r = Records::new(2);
        r.add(rec("e1", "a"));
        r.add(rec("e2", "b"));
        r.add(rec("e3", "a"));
        let all = r.list(None, None, 10);
        assert_eq!(all.iter().map(|f| f.environment.as_str()).collect::<Vec<_>>(), ["e3", "e2"]);
        assert_eq!(r.list(None, Some("a"), 10).len(), 1);
        assert_eq!(r.list(Some("e2"), None, 10).len(), 1);
    }
}
