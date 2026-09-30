package web

import (
	"crypto/subtle"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func (s *Server) authorizeMutation(writer http.ResponseWriter, request *http.Request) bool {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return false
	}
	if !sameOrigin(request, s.port) {
		writer.WriteHeader(http.StatusForbidden)
		return false
	}
	if !validToken(request, s.token) {
		writer.WriteHeader(http.StatusForbidden)
		return false
	}
	return true
}

func hostGuard(port int, next http.Handler) http.Handler {
	allowedHosts := map[string]struct{}{
		"127.0.0.1:" + strconv.Itoa(port): {},
		"localhost:" + strconv.Itoa(port): {},
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if _, ok := allowedHosts[request.Host]; !ok {
			writer.WriteHeader(http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func newToken(reader io.Reader) (string, error) {
	bytes := make([]byte, 32)
	if _, err := io.ReadFull(reader, bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func validToken(request *http.Request, expected string) bool {
	provided := request.Header.Get("X-Syl-Token")
	if provided == "" {
		provided = request.PostFormValue("token")
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func sameOrigin(request *http.Request, port int) bool {
	origins := request.Header.Values("Origin")
	switch len(origins) {
	case 0:
		return true
	case 1:
		break
	default:
		return false
	}
	origin, err := url.Parse(origins[0])
	if err != nil || !validOriginURL(origin) {
		return false
	}
	originPort := origin.Port()
	if originPort == "" {
		originPort = "80"
	}
	if originPort != strconv.Itoa(port) {
		return false
	}
	return isLoopbackOrigin(origin)
}

func validOriginURL(origin *url.URL) bool {
	if origin.Scheme != "http" {
		return false
	}
	if origin.Path != "" {
		return false
	}
	if origin.RawQuery != "" {
		return false
	}
	if origin.Fragment != "" {
		return false
	}
	return origin.User == nil
}

func isLoopbackOrigin(origin *url.URL) bool {
	hostname := strings.ToLower(origin.Hostname())
	return hostname == "127.0.0.1" || hostname == "localhost"
}
