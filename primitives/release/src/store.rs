//! Release records in a JSON-lines file. Each line is a full snapshot of one
//! release; the last line for an ID wins. The file is compacted at open.

use std::collections::HashMap;
use std::fs::{File, OpenOptions};
use std::io::{BufRead, BufReader, Write};
use std::path::{Path, PathBuf};
use std::sync::Mutex;

use serde::{Deserialize, Serialize};

use crate::api::{Release, ReleaseItem};

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
struct ItemJson {
    target: String,
    image: String,
    previous_image: String,
    status: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    message: Option<String>,
}

/// Stored form of a release. `rollback_of` is local: it names the release
/// that a rollback release restores.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
struct RecordJson {
    id: String,
    owner: String,
    status: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    reason: Option<String>,
    items: Vec<ItemJson>,
    created_ms: u64,
    updated_ms: u64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    rollback_of: Option<String>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct Record {
    pub release: Release,
    pub rollback_of: Option<String>,
}

impl From<&Record> for RecordJson {
    fn from(r: &Record) -> Self {
        let x = &r.release;
        RecordJson {
            id: x.id.clone(),
            owner: x.owner.clone(),
            status: x.status.clone(),
            reason: x.reason.clone(),
            items: x.items.iter().map(|i| ItemJson {
                target: i.target.clone(),
                image: i.image.clone(),
                previous_image: i.previous_image.clone(),
                status: i.status.clone(),
                message: i.message.clone(),
            }).collect(),
            created_ms: x.created_ms,
            updated_ms: x.updated_ms,
            rollback_of: r.rollback_of.clone(),
        }
    }
}

impl From<RecordJson> for Record {
    fn from(r: RecordJson) -> Self {
        Record {
            release: Release {
                id: r.id,
                owner: r.owner,
                status: r.status,
                reason: r.reason,
                items: r.items.into_iter().map(|i| ReleaseItem {
                    target: i.target,
                    image: i.image,
                    previous_image: i.previous_image,
                    status: i.status,
                    message: i.message,
                }).collect(),
                created_ms: r.created_ms,
                updated_ms: r.updated_ms,
            },
            rollback_of: r.rollback_of,
        }
    }
}

struct Inner {
    file: File,
    records: HashMap<String, Record>,
    order: Vec<String>,
}

pub struct Store {
    path: PathBuf,
    inner: Mutex<Inner>,
}

impl Store {
    /// Opens `<dir>/releases.jsonl`, loads it, and rewrites it compacted.
    pub fn open(dir: &Path) -> anyhow::Result<Self> {
        std::fs::create_dir_all(dir)?;
        let path = dir.join("releases.jsonl");
        let mut records = HashMap::new();
        let mut order = Vec::new();
        if let Ok(f) = File::open(&path) {
            for (n, line) in BufReader::new(f).lines().enumerate() {
                let line = line?;
                if line.trim().is_empty() {
                    continue;
                }
                // A torn last line after a crash is skipped; other bad lines stop startup.
                let r: RecordJson = match serde_json::from_str(&line) {
                    Ok(r) => r,
                    Err(e) => {
                        tracing::warn!(line = n + 1, error = %e, "skip unreadable release record");
                        continue;
                    }
                };
                if !records.contains_key(&r.id) {
                    order.push(r.id.clone());
                }
                records.insert(r.id.clone(), Record::from(r));
            }
        }
        let tmp = dir.join("releases.jsonl.tmp");
        {
            let mut f = File::create(&tmp)?;
            for id in &order {
                writeln!(f, "{}", serde_json::to_string(&RecordJson::from(&records[id]))?)?;
            }
            f.sync_all()?;
        }
        std::fs::rename(&tmp, &path)?;
        let file = OpenOptions::new().append(true).open(&path)?;
        Ok(Store { path, inner: Mutex::new(Inner { file, records, order }) })
    }

    pub fn path(&self) -> &Path {
        &self.path
    }

    /// Writes a snapshot and updates memory.
    pub fn put(&self, r: Record) -> anyhow::Result<()> {
        let mut g = self.inner.lock().unwrap();
        let line = serde_json::to_string(&RecordJson::from(&r))?;
        writeln!(g.file, "{line}")?;
        g.file.sync_data()?;
        if !g.records.contains_key(&r.release.id) {
            g.order.push(r.release.id.clone());
        }
        g.records.insert(r.release.id.clone(), r);
        Ok(())
    }

    pub fn get(&self, id: &str) -> Option<Record> {
        self.inner.lock().unwrap().records.get(id).cloned()
    }

    /// Newest first.
    pub fn list(&self, filter: impl Fn(&Release) -> bool, limit: usize) -> Vec<Release> {
        let g = self.inner.lock().unwrap();
        g.order.iter().rev().map(|id| &g.records[id].release).filter(|r| filter(r)).take(limit).cloned().collect()
    }

    /// Oldest first.
    pub fn all(&self) -> Vec<Record> {
        let g = self.inner.lock().unwrap();
        g.order.iter().map(|id| g.records[id].clone()).collect()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn rec(id: &str, status: &str) -> Record {
        Record {
            release: Release {
                id: id.into(), owner: "user:a".into(), status: status.into(), reason: None,
                items: vec![ReleaseItem { target: "t".into(), image: "i:2".into(), previous_image: "i:1".into(), status: status.into(), message: Some("m".into()) }],
                created_ms: 1, updated_ms: 2,
            },
            rollback_of: Some("rel_x".into()),
        }
    }

    #[test]
    fn reopen_keeps_last_snapshot() {
        let d = tempfile::tempdir().unwrap();
        let s = Store::open(d.path()).unwrap();
        s.put(rec("a", "pending")).unwrap();
        s.put(rec("b", "pending")).unwrap();
        s.put(rec("a", "succeeded")).unwrap();
        drop(s);
        std::fs::OpenOptions::new().append(true).open(d.path().join("releases.jsonl")).unwrap().write_all(b"{\"torn").unwrap();
        let s = Store::open(d.path()).unwrap();
        assert_eq!(s.get("a").unwrap(), rec("a", "succeeded"));
        assert_eq!(s.list(|_| true, 10).iter().map(|r| r.id.as_str()).collect::<Vec<_>>(), ["b", "a"]);
        assert_eq!(std::fs::read_to_string(s.path()).unwrap().lines().count(), 2);
    }
}
