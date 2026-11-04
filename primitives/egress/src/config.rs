//! Process settings. Each flag has an environment variable default.

use std::path::PathBuf;

#[derive(Debug, Clone)]
pub struct Settings {
    /// The API (EgressService).
    pub listen: String,
    /// The proxy port for environments.
    pub proxy_listen: String,
    pub auth_keys: Vec<PathBuf>,
    pub policy: PathBuf,
    pub ca_cert: PathBuf,
    pub ca_key: PathBuf,
    pub env_key: PathBuf,
    pub secrets_dir: Option<PathBuf>,
    pub upstream_ca: Option<PathBuf>,
    pub magi_url: Option<String>,
    pub magi_token: Option<PathBuf>,
    /// The SSH host key of the proxy (OpenSSH format). None turns SSH off.
    pub ssh_host_key: Option<PathBuf>,
}

/// `--name value` or `--name=value`, else the environment variable.
pub fn flag(args: &[String], name: &str, env: &str) -> Option<String> {
    let long = format!("--{name}");
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
}

fn addr(a: String) -> String {
    if a.starts_with(':') { format!("0.0.0.0{a}") } else { a }
}

impl Settings {
    pub fn from_args(args: &[String]) -> anyhow::Result<Self> {
        let get = |n: &str, e: &str| flag(args, n, e);
        let need = |n: &str, e: &str| get(n, e).ok_or_else(|| anyhow::anyhow!("--{n} ({e}) is required"));
        let s = Settings {
            listen: addr(get("listen", "MAGI_LISTEN").unwrap_or_else(|| ":8080".into())),
            proxy_listen: addr(get("proxy-listen", "MAGI_EGRESS_PROXY_LISTEN").unwrap_or_else(|| ":3128".into())),
            auth_keys: get("auth-keys", "MAGI_AUTH_KEYS")
                .map(|v| v.split(',').map(str::trim).filter(|s| !s.is_empty()).map(PathBuf::from).collect())
                .unwrap_or_default(),
            policy: need("policy", "MAGI_EGRESS_POLICY")?.into(),
            ca_cert: need("ca-cert", "MAGI_EGRESS_CA_CERT")?.into(),
            ca_key: need("ca-key", "MAGI_EGRESS_CA_KEY")?.into(),
            env_key: need("env-key", "MAGI_EGRESS_ENV_KEY")?.into(),
            secrets_dir: get("secrets-dir", "MAGI_EGRESS_SECRETS_DIR").map(PathBuf::from),
            upstream_ca: get("upstream-ca", "MAGI_EGRESS_UPSTREAM_CA").map(PathBuf::from),
            magi_url: get("magi-url", "MAGI_URL"),
            magi_token: get("magi-token", "MAGI_EGRESS_TOKEN_FILE").map(PathBuf::from),
            ssh_host_key: get("ssh-host-key", "MAGI_EGRESS_SSH_HOST_KEY").map(PathBuf::from),
        };
        anyhow::ensure!(s.magi_url.is_some() == s.magi_token.is_some(), "MAGI_URL and MAGI_EGRESS_TOKEN_FILE go together");
        Ok(s)
    }
}
