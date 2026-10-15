package api

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
)

func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !localRequest(r) {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		if r.Method == http.MethodGet && (r.URL.Path == "/healthz" || r.URL.Path == "/v1/identity") {
			next.ServeHTTP(w, r)
			return
		}
		if !bearerOK(r.Header.Get("Authorization"), s.Token) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func localRequest(r *http.Request) bool {
	if strings.TrimSpace(r.Header.Get("Origin")) != "" {
		return false
	}
	return localHost(r.Host)
}

func localHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func bearerOK(header, want string) bool {
	if want == "" {
		return false
	}
	const prefix = "Bearer "
	if len(header) < len(prefix) || header[:len(prefix)] != prefix {
		return false
	}
	got := header[len(prefix):]
	if len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
