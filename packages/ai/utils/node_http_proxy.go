// This file is a Go port of packages/ai/src/utils/node-http-proxy.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package utils

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/minifish-org/pith/packages/ai/types"
)

var defaultProxyPorts = map[string]int{
	"ftp":    21,
	"gopher": 70,
	"http":   80,
	"https":  443,
	"ws":     80,
	"wss":    443,
}

// getProxyEnv reads a proxy environment variable in lowercase then uppercase
// form from the scoped env, then the process environment.
func getProxyEnv(key string, env types.ProviderEnv) string {
	lowercaseKey := strings.ToLower(key)
	uppercaseKey := strings.ToUpper(key)
	read := func(name string) (string, bool) {
		if env != nil {
			if value, ok := env[name]; ok && value != nil && *value != "" {
				return *value, true
			}
		}
		if value := GetProviderEnvValue(name, nil); value != nil && *value != "" {
			return *value, true
		}
		return "", false
	}
	if value, ok := read(lowercaseKey); ok {
		return value
	}
	if value, ok := read(uppercaseKey); ok {
		return value
	}
	return ""
}

func parseProxyTargetURL(targetURL string) (*url.URL, bool) {
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return nil, false
	}
	return parsed, true
}

func stripBrackets(host string) string {
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return host[1 : len(host)-1]
	}
	return host
}

type noProxyEntry struct {
	host string
	port int
}

func parseNoProxyEntry(entry string) (noProxyEntry, bool) {
	trimmed := strings.ToLower(strings.TrimSpace(entry))
	if trimmed == "" {
		return noProxyEntry{}, false
	}

	if strings.HasPrefix(trimmed, "[") {
		if closingBracket := strings.Index(trimmed, "]"); closingBracket != -1 {
			host := trimmed[1:closingBracket]
			rest := trimmed[closingBracket+1:]
			if strings.HasPrefix(rest, ":") {
				port, err := strconv.Atoi(rest[1:])
				if err != nil {
					return noProxyEntry{host: host, port: 0}, true
				}
				return noProxyEntry{host: host, port: port}, true
			}
			return noProxyEntry{host: host, port: 0}, true
		}
	}

	if strings.Contains(trimmed, ":") && strings.Count(trimmed, ":") > 1 {
		return noProxyEntry{host: trimmed, port: 0}, true
	}

	if colonIndex := strings.LastIndex(trimmed, ":"); colonIndex != -1 && colonIndex == strings.Index(trimmed, ":") {
		host := trimmed[:colonIndex]
		if port, err := strconv.Atoi(trimmed[colonIndex+1:]); err == nil {
			return noProxyEntry{host: host, port: port}, true
		}
	}

	return noProxyEntry{host: trimmed, port: 0}, true
}

func shouldProxyHostname(hostname string, port int, env types.ProviderEnv) bool {
	noProxy := strings.ToLower(getProxyEnv("no_proxy", env))
	if noProxy == "" {
		return true
	}
	if noProxy == "*" {
		return false
	}

	normalizedTargetHost := stripBrackets(strings.ToLower(hostname))

	for _, rawEntry := range splitNoProxy(noProxy) {
		parsed, ok := parseNoProxyEntry(rawEntry)
		if !ok {
			continue
		}
		if parsed.port != 0 && parsed.port != port {
			continue
		}

		domain := stripBrackets(parsed.host)
		if strings.HasPrefix(domain, "*.") {
			domain = domain[2:]
		} else if strings.HasPrefix(domain, ".") || strings.HasPrefix(domain, "*") {
			domain = domain[1:]
		}

		if domain == "" {
			continue
		}
		if normalizedTargetHost == domain || strings.HasSuffix(normalizedTargetHost, "."+domain) {
			return false
		}
	}
	return true
}

func splitNoProxy(noProxy string) []string {
	return strings.FieldsFunc(noProxy, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r' })
}

func getProxyForURL(targetURL string, env types.ProviderEnv) string {
	parsed, ok := parseProxyTargetURL(targetURL)
	if !ok || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}

	protocol := strings.SplitN(parsed.Scheme, ":", 2)[0]
	hostname := parsed.Hostname()
	if hostname == "" {
		hostname = stripBrackets(stripPort(parsed.Host))
	}
	hostname = stripBrackets(hostname)
	port := 0
	if parsed.Port() != "" {
		if p, err := strconv.Atoi(parsed.Port()); err == nil {
			port = p
		}
	}
	if port == 0 {
		if defaultPort, ok := defaultProxyPorts[protocol]; ok {
			port = defaultPort
		}
	}
	if !shouldProxyHostname(hostname, port, env) {
		return ""
	}

	proxy := getProxyEnv(protocol+"_proxy", env)
	if proxy == "" {
		proxy = getProxyEnv("all_proxy", env)
	}
	if proxy != "" && !strings.Contains(proxy, "://") {
		proxy = protocol + "://" + proxy
	}
	return proxy
}

func stripPort(host string) string {
	if strings.HasPrefix(host, "[") {
		if closing := strings.Index(host, "]"); closing != -1 {
			return host[:closing+1]
		}
	}
	if colon := strings.LastIndex(host, ":"); colon != -1 && strings.Count(host, ":") == 1 {
		return host[:colon]
	}
	return host
}

// UnsupportedProxyProtocolMessage is returned when a proxy URL uses SOCKS or PAC.
const UnsupportedProxyProtocolMessage = "Unsupported proxy protocol. SOCKS and PAC proxy URLs are not supported; use an HTTP or HTTPS proxy URL."

// ResolveHttpProxyUrlForTarget resolves the HTTP/HTTPS proxy URL for a target
// URL, honoring the no_proxy exclusions. It returns nil when no proxy applies.
func ResolveHttpProxyUrlForTarget(targetURL string, env types.ProviderEnv) (*url.URL, error) {
	proxy := getProxyForURL(targetURL, env)
	if proxy == "" {
		return nil, nil
	}

	proxyURL, err := url.Parse(proxy)
	if err != nil {
		return nil, fmt.Errorf("Invalid proxy URL %q: %v", proxy, err)
	}

	if proxyURL.Scheme != "http" && proxyURL.Scheme != "https" {
		return nil, fmt.Errorf("%s Got %s", UnsupportedProxyProtocolMessage, proxyURL.Scheme)
	}

	return proxyURL, nil
}
