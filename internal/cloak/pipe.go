package cloak

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"time"
)

// Chromium's CDP pipe uses file descriptors 3/4 and NUL-delimited JSON, as in
// Playwright. Rod's CDP client accepts this transport without a listening port.
type pipeTransport struct {
	reader      *bufio.Reader
	read, write *os.File
	mu          sync.Mutex
}

func (p *pipeTransport) Send(data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	message := append(append([]byte(nil), data...), 0)
	n, err := p.write.Write(message)
	if err == nil && n != len(message) {
		return io.ErrShortWrite
	}
	return err
}

func (p *pipeTransport) Read() ([]byte, error) {
	data, err := p.reader.ReadBytes(0)
	if err != nil {
		return nil, err
	}
	return data[:len(data)-1], nil
}

func launchPipe(binary string, args []string) (*exec.Cmd, <-chan error, *pipeTransport, error) {
	if runtime.GOOS == "windows" {
		return nil, nil, nil, os.ErrInvalid
	}
	childRead, parentWrite, err := os.Pipe()
	if err != nil {
		return nil, nil, nil, err
	}
	parentRead, childWrite, err := os.Pipe()
	if err != nil {
		childRead.Close()
		parentWrite.Close()
		return nil, nil, nil, err
	}
	defer childRead.Close()
	defer childWrite.Close()
	process := exec.Command(binary, args...)
	process.ExtraFiles = []*os.File{childRead, childWrite}
	if err = process.Start(); err != nil {
		parentRead.Close()
		parentWrite.Close()
		return nil, nil, nil, err
	}
	done := make(chan error, 1)
	go func() { done <- process.Wait(); close(done) }()
	transport := &pipeTransport{reader: bufio.NewReader(parentRead), read: parentRead, write: parentWrite}
	return process, done, transport, nil
}

func stopPipe(process *exec.Cmd, done <-chan error, pipe *pipeTransport) {
	defer pipe.read.Close()
	defer pipe.write.Close()
	select {
	case <-done:
		return
	default:
	}
	// Browser.Close gets the first chance to release the license seat cleanly.
	// Terminate only when that command failed or the process does not exit.
	select {
	case <-done:
		return
	case <-time.After(3 * time.Second):
	}
	_ = process.Process.Signal(syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = process.Process.Kill()
		<-done
	}
}
