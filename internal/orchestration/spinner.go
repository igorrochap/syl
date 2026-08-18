package orchestration

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/mattn/go-isatty"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const spinnerInterval = 100 * time.Millisecond

// isTerminalFd reports whether a file descriptor is a real terminal. It is a
// package variable so tests can drive the spinner without a PTY.
var isTerminalFd = isatty.IsTerminal

// spinner renders a "Working" animation on the current line while a quiet-mode
// harness run is silently doing work (e.g. running tools) with nothing visible
// to show for it yet. It is disabled on non-terminal output, so redirected
// output (files, pipes, tests) is never touched.
//
// The animation is written straight to the terminal, bypassing any
// line-prefixing wrapper (such as roleLabelWriter): its cursor-control escapes
// are transient and must never be turned into labeled, permanent lines.
type spinner struct {
	output  io.Writer
	enabled bool

	mu     sync.Mutex
	active bool
	stopCh chan struct{}
	doneCh chan struct{}
}

func newSpinner(output io.Writer) *spinner {
	terminal, ok := terminalWriter(output)
	return &spinner{output: terminal, enabled: ok}
}

// terminalWriter unwraps output down to the writer ultimately backed by a real
// terminal, following any Unwrap method (the same convention errors.Unwrap
// uses). It returns that terminal-backed writer so the spinner can draw on it
// directly, and reports whether output is a terminal at all.
func terminalWriter(output io.Writer) (io.Writer, bool) {
	for {
		if file, ok := output.(interface{ Fd() uintptr }); ok {
			if isTerminalFd(file.Fd()) {
				return output, true
			}
			return nil, false
		}
		unwrapper, ok := output.(interface{ Unwrap() io.Writer })
		if !ok {
			return nil, false
		}
		output = unwrapper.Unwrap()
	}
}

// Start begins animating "Working" on the current line. It is safe to call
// repeatedly; a call while already active is a no-op.
func (s *spinner) Start() {
	if s == nil || !s.enabled {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active {
		return
	}
	s.active = true
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	s.stopCh = stopCh
	s.doneCh = doneCh
	go s.run(stopCh, doneCh)
}

func (s *spinner) run(stopCh, doneCh chan struct{}) {
	defer close(doneCh)
	ticker := time.NewTicker(spinnerInterval)
	defer ticker.Stop()
	frame := 0
	// Draw the first frame immediately so short-lived work still shows the
	// animation instead of waiting a full interval for the first tick.
	s.draw(frame)
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			frame++
			s.draw(frame)
		}
	}
}

func (s *spinner) draw(frame int) {
	fmt.Fprintf(s.output, "\r\x1b[K%s Working", spinnerFrames[frame%len(spinnerFrames)])
}

// Stop halts the animation and clears its line, leaving the cursor at the start
// of the now-empty line so the next output writes there. It is safe to call
// repeatedly; a call while inactive is a no-op.
func (s *spinner) Stop() {
	if s == nil || !s.enabled {
		return
	}
	s.mu.Lock()
	if !s.active {
		s.mu.Unlock()
		return
	}
	s.active = false
	stopCh, doneCh := s.stopCh, s.doneCh
	s.mu.Unlock()
	close(stopCh)
	<-doneCh
	fmt.Fprint(s.output, "\r\x1b[K")
}
