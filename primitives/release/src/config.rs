//! Process settings and the targets file.

use std::path::{Path, PathBuf};
use std::time::Duration;

use serde::Deserialize;

use crate::api::Target;

/// One configured target. Fields other than the contract `Target` fields
/// stay local to the controller.
#[derive(Debug, Clone, PartialEq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct TargetConfig {
    pub name: String,
    pub namespace: String,
    pub deployment: String,
    pub container: String,
    pub health_path: Option<String>,
    pub health_port: Option<u64>,
    /// Rollout timeout in seconds.
    pub timeout_seconds: Option<u64>,
    /// Stabilization window in seconds.
    pub stabilize_seconds: Option<u64>,
    /// Arguments of a preflight run: before the controller changes the
    /// Deployment, it runs the new image once with these arguments, in a
    /// pod with the pod spec of the Deployment. Exit code 0 passes.
    #[serde(default)]
    pub preflight_args: Option<Vec<String>>,
    /// Time limit of the preflight run in seconds (default 120).
    #[serde(default)]
    pub preflight_timeout_seconds: Option<u64>,
}

impl TargetConfig {
    pub fn to_api(&self) -> Target {
        Target {
            name: self.name.clone(),
            namespace: self.namespace.clone(),
            deployment: self.deployment.clone(),
            container: self.container.clone(),
            health_path: self.health_path.clone(),
            health_port: self.health_port,
        }
    }
}

#[derive(Deserialize)]
#[serde(untagged)]
enum TargetsFile {
    Wrapped { targets: Vec<TargetConfig> },
    List(Vec<TargetConfig>),
}

/// Parses YAML or JSON: either a list of targets or `{targets: [...]}`.
pub fn parse_targets(text: &str) -> anyhow::Result<Vec<TargetConfig>> {
    let ts = match serde_yaml::from_str::<TargetsFile>(text)? {
        TargetsFile::Wrapped { targets } | TargetsFile::List(targets) => targets,
    };
    let mut seen = std::collections::HashSet::new();
    for t in &ts {
        for (k, v) in [("name", &t.name), ("namespace", &t.namespace), ("deployment", &t.deployment), ("container", &t.container)] {
            anyhow::ensure!(!v.is_empty(), "target {:?}: {k} is required", t.name);
        }
        anyhow::ensure!(seen.insert(t.name.clone()), "target {:?} is listed twice", t.name);
        if let Some(a) = &t.preflight_args {
            anyhow::ensure!(!a.is_empty(), "target {:?}: preflight_args must not be empty", t.name);
        }
        if let Some(p) = &t.health_path {
            anyhow::ensure!(p.starts_with('/'), "target {:?}: health_path must start with /", t.name);
        }
    }
    Ok(ts)
}

pub fn load_targets(path: &Path) -> anyhow::Result<Vec<TargetConfig>> {
    let text = std::fs::read_to_string(path)
        .map_err(|e| anyhow::anyhow!("targets file {}: {e}", path.display()))?;
    parse_targets(&text)
}

#[derive(Debug, Clone)]
pub struct Settings {
    pub listen: String,
    pub auth_keys: Vec<PathBuf>,
    pub targets: PathBuf,
    pub data: PathBuf,
    pub timeout: Duration,
    pub stabilize: Duration,
    pub health_via_proxy: bool,
    pub kube_context: Option<String>,
    /// The watch: the namespace, the CloudNativePG cluster of the
    /// database, and the label (name, value) of the pods of the services.
    pub watch_namespace: Option<String>,
    pub watch_database: Option<String>,
    pub watch_services: Option<(String, String)>,
    /// A database instance that is not ready for this long is restarted.
    pub database_restart_after: Duration,
    /// A service that is not ready for this long is restarted.
    pub service_restart_after: Duration,
    /// A workload that is not ready this long after the restart is
    /// "unhealthy".
    pub give_up_after: Duration,
}

impl Settings {
    /// Reads `--name value` / `--name=value` flags. Each flag defaults to its
    /// environment variable.
    pub fn from_args(args: &[String]) -> anyhow::Result<Self> {
        let get = |flag: &str, env: &str| -> Option<String> {
            let long = format!("--{flag}");
            let mut it = args.iter();
            while let Some(a) = it.next() {
                if *a == long {
                    return it.next().cloned();
                }
                if let Some(v) = a.strip_prefix(&format!("{long}=")) {
                    return Some(v.to_string());
                }
            }
            std::env::var(env).ok().filter(|v| !v.is_empty())
        };
        let secs = |flag: &str, env: &str, def: u64| -> anyhow::Result<Duration> {
            Ok(Duration::from_secs(match get(flag, env) {
                Some(v) => v.parse().map_err(|_| anyhow::anyhow!("{env}: expected seconds"))?,
                None => def,
            }))
        };
        let mut listen = get("listen", "MAGI_LISTEN").unwrap_or_else(|| ":8080".into());
        if listen.starts_with(':') {
            listen = format!("0.0.0.0{listen}");
        }
        let health = get("health", "MAGI_RELEASE_HEALTH").unwrap_or_else(|| "direct".into());
        anyhow::ensure!(health == "direct" || health == "proxy", "MAGI_RELEASE_HEALTH: direct or proxy");
        // MAGI_RELEASE_DATABASE is namespace/cluster. MAGI_RELEASE_SERVICES
        // is namespace/label=value.
        let split = |env: &str, v: String| -> anyhow::Result<(String, String)> {
            match v.split_once('/') {
                Some((ns, rest)) if !ns.is_empty() && !rest.is_empty() => Ok((ns.to_string(), rest.to_string())),
                _ => anyhow::bail!("{env}: expected namespace/name"),
            }
        };
        let database = get("database", "MAGI_RELEASE_DATABASE").map(|v| split("MAGI_RELEASE_DATABASE", v)).transpose()?;
        let services = get("services", "MAGI_RELEASE_SERVICES").map(|v| split("MAGI_RELEASE_SERVICES", v)).transpose()?;
        let watch_services = match &services {
            Some((_, l)) => match l.split_once('=') {
                Some((k, v)) if !k.is_empty() && !v.is_empty() => Some((k.to_string(), v.to_string())),
                _ => anyhow::bail!("MAGI_RELEASE_SERVICES: expected namespace/label=value"),
            },
            None => None,
        };
        if let (Some((a, _)), Some((b, _))) = (&database, &services) {
            anyhow::ensure!(a == b, "MAGI_RELEASE_DATABASE and MAGI_RELEASE_SERVICES must name one namespace");
        }
        let watch_namespace = database.as_ref().or(services.as_ref()).map(|(ns, _)| ns.clone());
        Ok(Settings {
            listen,
            watch_namespace,
            watch_database: database.map(|(_, name)| name),
            watch_services,
            database_restart_after: secs("database-restart-after", "MAGI_RELEASE_DATABASE_RESTART_AFTER", 90)?,
            service_restart_after: secs("service-restart-after", "MAGI_RELEASE_SERVICE_RESTART_AFTER", 180)?,
            give_up_after: secs("give-up-after", "MAGI_RELEASE_GIVE_UP_AFTER", 600)?,
            auth_keys: get("auth-keys", "MAGI_AUTH_KEYS")
                .map(|v| v.split(',').map(str::trim).filter(|s| !s.is_empty()).map(PathBuf::from).collect())
                .unwrap_or_default(),
            targets: get("targets", "MAGI_RELEASE_TARGETS").unwrap_or_else(|| "/etc/magi/release/targets.yaml".into()).into(),
            data: get("data", "MAGI_RELEASE_DATA").unwrap_or_else(|| "/var/lib/magi-release".into()).into(),
            timeout: secs("timeout", "MAGI_RELEASE_TIMEOUT", 180)?,
            stabilize: secs("stabilize", "MAGI_RELEASE_STABILIZE", 20)?,
            health_via_proxy: health == "proxy",
            kube_context: get("kube-context", "MAGI_KUBE_CONTEXT"),
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_both_forms() {
        let y = "targets:\n- {name: web, namespace: magi, deployment: web, container: web, health_path: /healthz, health_port: 8080}\n";
        let t = parse_targets(y).unwrap();
        assert_eq!(t[0].health_port, Some(8080));
        let j = r#"[{"name":"a","namespace":"n","deployment":"d","container":"c"}]"#;
        assert_eq!(parse_targets(j).unwrap()[0].name, "a");
        assert!(parse_targets(r#"[{"name":"a","namespace":"n","deployment":"d","container":"c"},{"name":"a","namespace":"n","deployment":"d","container":"c"}]"#).is_err());
        assert!(parse_targets(r#"[{"name":"a","namespace":"","deployment":"d","container":"c"}]"#).is_err());
    }

    #[test]
    fn watch_settings() {
        let args = |v: &[&str]| v.iter().map(|s| s.to_string()).collect::<Vec<_>>();
        let s = Settings::from_args(&args(&["--database", "magi/magi-db", "--services", "magi/app.kubernetes.io/part-of=magi"])).unwrap();
        assert_eq!(s.watch_namespace.as_deref(), Some("magi"));
        assert_eq!(s.watch_database.as_deref(), Some("magi-db"));
        assert_eq!(s.watch_services, Some(("app.kubernetes.io/part-of".to_string(), "magi".to_string())));
        assert!(Settings::from_args(&args(&["--database", "magi-db"])).is_err());
        assert!(Settings::from_args(&args(&["--services", "magi/part-of"])).is_err());
        assert!(Settings::from_args(&args(&["--database", "a/db", "--services", "b/k=v"])).is_err());
    }
}
