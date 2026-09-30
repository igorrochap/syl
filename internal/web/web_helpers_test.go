package web

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrochap/syl/internal/readmodel"
	"github.com/igorrochap/syl/internal/sylhome"
)

func TestHandlerRoutesContentAndNotFound(t *testing.T) {
	server, err := New(testSylHome(t), 7777)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()

	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7777/overview/content", nil)
	request.Host = "127.0.0.1:7777"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("content status = %d, want 200", recorder.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7777/missing", nil)
	request.Host = "127.0.0.1:7777"
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing route status = %d, want 404", recorder.Code)
	}
}

func TestHandlerRendersReadModelErrors(t *testing.T) {
	server, err := New(testSylHome(t), 7777)
	if err != nil {
		t.Fatal(err)
	}
	server.model = func() (readmodel.Overview, error) { return readmodel.Overview{}, errors.New("read failed") }
	handler := server.Handler()
	for _, path := range []string{"/", "/overview/content"} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7777"+path, nil)
			request.Host = "127.0.0.1:7777"
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusInternalServerError || recorder.Body.String() != "syl ui: read failed" {
				t.Fatalf("status/body = %d/%q", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestServeRejectsMissingListenerAndReturnsServeError(t *testing.T) {
	server, err := New(testSylHome(t), 7777)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Serve(context.Background(), nil); err == nil {
		t.Fatal("Serve(nil) error = nil, want missing-listener error")
	}

	listener := &errorListener{err: errors.New("accept failed")}
	if err := server.Serve(context.Background(), listener); err == nil || !strings.Contains(err.Error(), "accept failed") {
		t.Fatalf("Serve(error listener) error = %v, want accept failure", err)
	}
}

func TestServeReportsShutdownError(t *testing.T) {
	server, err := New(testSylHome(t), 7777)
	if err != nil {
		t.Fatal(err)
	}
	listener := &closeErrorListener{closed: make(chan struct{}), ready: make(chan struct{}), err: errors.New("close failed")}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- server.Serve(ctx, listener) }()
	<-listener.ready
	cancel()
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "shut down web server") {
			t.Fatalf("Serve() error = %v, want shutdown error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve() did not stop after context cancellation")
	}
}

type errorListener struct {
	err error
}

func (listener *errorListener) Accept() (net.Conn, error) { return nil, listener.err }
func (*errorListener) Close() error                       { return nil }
func (*errorListener) Addr() net.Addr                     { return testAddress("127.0.0.1:7777") }

type closeErrorListener struct {
	once      sync.Once
	readyOnce sync.Once
	closed    chan struct{}
	ready     chan struct{}
	err       error
}

func (listener *closeErrorListener) Accept() (net.Conn, error) {
	listener.readyOnce.Do(func() { close(listener.ready) })
	<-listener.closed
	return nil, errors.New("accept after close")
}

func (listener *closeErrorListener) Close() error {
	listener.once.Do(func() { close(listener.closed) })
	return listener.err
}

func (*closeErrorListener) Addr() net.Addr { return testAddress("127.0.0.1:7777") }

func testSylHome(t *testing.T) sylhome.Dir {
	t.Helper()
	dir, err := sylhome.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

type testAddress string

func (address testAddress) Network() string { return "tcp" }
func (address testAddress) String() string  { return string(address) }
