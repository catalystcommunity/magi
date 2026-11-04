// Package egressenv holds what a client of the Magi egress proxy
// (primitives/egress) needs: the proxy password of an environment and the
// environment variables that send common tools through the proxy.
package egressenv

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
)

// Credential is the proxy password of environment env. The proxy checks
// it with the same key (primitives/egress/src/proxy.rs, credential).
func Credential(key []byte, env string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("magi-egress-env:"))
	m.Write([]byte(env))
	return hex.EncodeToString(m.Sum(nil))
}

// URL is the proxy URL of env at host:port with its credential.
func URL(hostport, env, credential string) string {
	u := url.URL{Scheme: "http", User: url.UserPassword(env, credential), Host: hostport}
	return u.String()
}

// CAVars names the variables that common tools read for a CA file. The
// proxy CA is the only CA that an environment needs: all TLS ends there.
var CAVars = []string{"SSL_CERT_FILE", "REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE", "NODE_EXTRA_CA_CERTS", "GIT_SSL_CAINFO", "PIP_CERT", "CARGO_HTTP_CAINFO"}

// ProxyVars names the proxy variables.
var ProxyVars = []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"}

// Vars returns the variables for proxyURL and the CA file caFile.
func Vars(proxyURL, caFile string) map[string]string {
	v := map[string]string{"NO_PROXY": "localhost,127.0.0.1,::1", "no_proxy": "localhost,127.0.0.1,::1"}
	for _, n := range ProxyVars {
		v[n] = proxyURL
	}
	for _, n := range CAVars {
		v[n] = caFile
	}
	return v
}
