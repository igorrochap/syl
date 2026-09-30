package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewTokenReportsReaderError(t *testing.T) {
	if _, err := newToken(failingReader{}); err == nil {
		t.Fatal("newToken() succeeded with a failing reader")
	}
}

func TestSameOriginValidatesAllOriginComponents(t *testing.T) {
	for _, test := range []struct {
		name    string
		origins []string
		want    bool
	}{
		{name: "absent", want: true},
		{name: "own origin", origins: []string{"http://localhost:7777"}, want: true},
		{name: "multiple", origins: []string{"http://localhost:7777", "http://127.0.0.1:7777"}},
		{name: "malformed", origins: []string{"http://[::1"}},
		{name: "wrong scheme", origins: []string{"https://localhost:7777"}},
		{name: "path", origins: []string{"http://localhost:7777/path"}},
		{name: "query", origins: []string{"http://localhost:7777?query"}},
		{name: "fragment", origins: []string{"http://localhost:7777#fragment"}},
		{name: "user info", origins: []string{"http://user@localhost:7777"}},
		{name: "implicit port", origins: []string{"http://localhost"}},
		{name: "wrong port", origins: []string{"http://localhost:7778"}},
		{name: "foreign host", origins: []string{"http://example.test:7777"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:7777/", nil)
			for _, origin := range test.origins {
				request.Header.Add("Origin", origin)
			}
			if got := sameOrigin(request, 7777); got != test.want {
				t.Fatalf("sameOrigin() = %v, want %v", got, test.want)
			}
		})
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("random source failed")
}

func TestHandlerRejectsUntrustedHostWithoutPageContent(t *testing.T) {
	called := false
	handler := hostGuard(7777, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	request := httptest.NewRequest(http.MethodGet, "http://evil.example:7777/", nil)
	request.Host = "evil.example:7777"
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusMisdirectedRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusMisdirectedRequest)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("body = %q, want no page content", recorder.Body.String())
	}
	if called {
		t.Fatal("host guard called the next handler for an untrusted Host")
	}
}
