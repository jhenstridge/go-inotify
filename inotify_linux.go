// Copyright 2010 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

/*
Package inotify implements a wrapper for the Linux inotify system.

Example:
    watcher, err := inotify.NewWatcher()
    if err != nil {
        log.Fatal(err)
    }
    err = watcher.Watch("/tmp")
    if err != nil {
        log.Fatal(err)
    }
    for {
        select {
        case ev := <-watcher.Event:
            log.Println("event:", ev)
        case err := <-watcher.Error:
            log.Println("error:", err)
        }
    }

*/
package inotify // import "github.com/jhenstridge/go-inotify"

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall"
	"unsafe"
)

var (
	ErrClosed       = errors.New("inotify instance already closed")
	ErrInvalidWatch = errors.New("invalid inotify watch")
)

type Event struct {
	Watch  *Watch // the watch associated with this event
	Mask   Mask   // Mask of events
	Cookie uint32 // Unique cookie associating related events (for rename(2))
	Name   string // File name (optional)
}

type Watch struct {
	wd   int32  // Watch descriptor (as returned by the inotify_add_watch() syscall)
	Mask Mask   // inotify flags of this watch (see inotify(7) for the list of valid flags)
	Path string // the path associated with this watch
}

type Mask uint32

type Watcher struct {
	mu       sync.Mutex
	fd       int              // File descriptor (as returned by the inotify_init() syscall)
	f        *os.File         // A os.File for the above file descriptor
	watches  map[int32]*Watch // Map of inotify watches (key: wd)
	error    chan error       // Errors are sent on this channel
	Event    chan Event       // Events are returned on this channel
	done     chan struct{}    // Channel for sending a "quit message" to the reader goroutine
	isClosed bool             // Set to true when Close() is first called
}

// NewWatcher creates and returns a new inotify instance using inotify_init(2)
func NewWatcher() (*Watcher, error) {
	fd, errno := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK)
	if fd == -1 {
		return nil, os.NewSyscallError("inotify_init", errno)
	}
	w := &Watcher{
		fd:      fd,
		f:       os.NewFile(uintptr(fd), ""),
		watches: make(map[int32]*Watch),
		Event:   make(chan Event),
		error:   make(chan error, 1),
		done:    make(chan struct{}),
	}

	go w.readEvents()
	return w, nil
}

// Close closes an inotify watcher instance
// It sends a message to the reader goroutine to quit and removes all watches
// associated with the inotify instance
func (w *Watcher) Close() error {
	w.mu.Lock()
	if !w.isClosed {
		// Send "quit" message to the reader goroutine
		close(w.done)
		w.f.Close()
	}
	w.isClosed = true

	w.mu.Unlock()
	return <-w.error
}

// AddWatch adds path to the watched file set.
// The mask are interpreted as described in inotify_add_watch(2).
//
// If a watch already exists for the path's inode, a number of things
// could happen:
//
//   1. If the mask includes IN_MASK_CREATE, an error will be returned.
//   2. If the mask includes IN_MASK_ADD, the new mask bits will be
//      combined with the old.
//   3. Otherwise, the old mask will be replaced with the new one.

func (w *Watcher) AddWatch(path string, mask Mask) (*Watch, error) {
	w.mu.Lock() // synchronize with readEvents goroutine
	defer w.mu.Unlock()

	if w.isClosed {
		return nil, ErrClosed
	}

	wd, err := syscall.InotifyAddWatch(w.fd, path, uint32(mask))
	if err != nil {
		return nil, &os.PathError{
			Op:   "inotify_add_watch",
			Path: path,
			Err:  err,
		}
	}

	watch := w.watches[int32(wd)]
	if watch == nil {
		watch = &Watch{
			wd: int32(wd),
		}
		w.watches[watch.wd] = watch
	}

	newMask := mask & (IN_ALL_EVENTS | IN_DONT_FOLLOW | IN_EXCL_UNLINK)
	if mask&IN_MASK_ADD != 0 {
		newMask |= watch.Mask
	}
	watch.Mask = newMask
	watch.Path = path

	return watch, nil
}

// Watch adds path to the watched file set, watching all events.
func (w *Watcher) Watch(path string) (*Watch, error) {
	return w.AddWatch(path, IN_ALL_EVENTS)
}

// RemoveWatch removes path from the watched file set.
func (w *Watcher) RemoveWatch(watch *Watch) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.isClosed {
		return ErrClosed
	}
	if watch.wd < 0 || w.watches[watch.wd] != watch {
		return ErrInvalidWatch
	}

	_, err := syscall.InotifyRmWatch(w.fd, uint32(watch.wd))
	if err != nil {
		return os.NewSyscallError("inotify_rm_watch", err)
	}

	// We don't remove the watch from the watches map here, since
	// pending events might still reference it. Instead, we give
	// it an invalid watch ID so we can catch attempts to remove
	// it twice.
	watch.wd = -1
	return nil
}

func (w *Watcher) decodeEvents(events []Event, buf []byte) []Event {
	w.mu.Lock()
	defer w.mu.Unlock()

	offset := 0
	// We don't know how many events we just read into the buffer
	// While the offset points to at least one whole event...
	for offset+syscall.SizeofInotifyEvent <= len(buf) {
		// Point "raw" to the event in the buffer
		raw := (*syscall.InotifyEvent)(unsafe.Pointer(&buf[offset]))
		offset += syscall.SizeofInotifyEvent

		var watch *Watch
		if raw.Wd >= 0 {
			watch = w.watches[raw.Wd]
		}
		var name string
		if raw.Len != 0 {
			name = string(bytes.TrimRight(buf[offset:offset+int(raw.Len)], "\x00"))
		}
		offset += int(raw.Len)

		events = append(events, Event{
			Watch:  watch,
			Mask:   Mask(raw.Mask),
			Cookie: raw.Cookie,
			Name:   name,
		})

		if raw.Mask&syscall.IN_IGNORED != 0 && watch != nil {
			delete(w.watches, watch.wd)
			watch.wd = -1
		}
	}
	return events
}

// readEvents reads from the inotify file descriptor, converts the
// received events into Event objects and sends them via the Event channel
func (w *Watcher) readEvents() {
	var (
		buf    [4096]byte
		events []Event
		retErr error
	)
	defer func() {
		w.error <- retErr
		close(w.error)
		close(w.Event)
	}()

	for {
		n, err := w.f.Read(buf[:])

		if n > 0 {
			events = w.decodeEvents(events[:0], buf[:n])
			for i := range events {
				select {
				case w.Event <- events[i]:
				case <-w.done:
					return
				}
			}
		}

		if err != nil {
			if err != io.EOF && !errors.Is(err, os.ErrClosed) {
				retErr = err
			}
			return
		}
	}
}

// String formats the event e in the form
// "filename: 0xEventMask = IN_ACCESS|IN_ATTRIB_|..."
func (e Event) String() string {
	var events string = ""

	m := e.Mask
	for _, b := range eventBits {
		if m&b.Value == b.Value {
			m &^= b.Value
			events += "|" + b.Name
		}
	}

	if m != 0 {
		events += fmt.Sprintf("|%#x", m)
	}
	if len(events) > 0 {
		events = " == " + events[1:]
	}

	return fmt.Sprintf("%q: %#x%s", e.Name, e.Mask, events)
}

const (
	// Options for inotify_init() are not exported
	// IN_CLOEXEC    Mask = syscall.IN_CLOEXEC
	// IN_NONBLOCK   Mask = syscall.IN_NONBLOCK

	// Options for AddWatch
	IN_DONT_FOLLOW Mask = syscall.IN_DONT_FOLLOW
	IN_EXCL_UNLINK Mask = syscall.IN_EXCL_UNLINK
	IN_ONESHOT     Mask = syscall.IN_ONESHOT
	IN_ONLYDIR     Mask = syscall.IN_ONLYDIR

	IN_MASK_ADD Mask = syscall.IN_MASK_ADD

	// Events
	IN_ACCESS        Mask = syscall.IN_ACCESS
	IN_ALL_EVENTS    Mask = syscall.IN_ALL_EVENTS
	IN_ATTRIB        Mask = syscall.IN_ATTRIB
	IN_CLOSE         Mask = syscall.IN_CLOSE
	IN_CLOSE_NOWRITE Mask = syscall.IN_CLOSE_NOWRITE
	IN_CLOSE_WRITE   Mask = syscall.IN_CLOSE_WRITE
	IN_CREATE        Mask = syscall.IN_CREATE
	IN_DELETE        Mask = syscall.IN_DELETE
	IN_DELETE_SELF   Mask = syscall.IN_DELETE_SELF
	IN_MODIFY        Mask = syscall.IN_MODIFY
	IN_MOVE          Mask = syscall.IN_MOVE
	IN_MOVED_FROM    Mask = syscall.IN_MOVED_FROM
	IN_MOVED_TO      Mask = syscall.IN_MOVED_TO
	IN_MOVE_SELF     Mask = syscall.IN_MOVE_SELF
	IN_OPEN          Mask = syscall.IN_OPEN

	// Special events
	IN_ISDIR      Mask = syscall.IN_ISDIR
	IN_IGNORED    Mask = syscall.IN_IGNORED
	IN_Q_OVERFLOW Mask = syscall.IN_Q_OVERFLOW
	IN_UNMOUNT    Mask = syscall.IN_UNMOUNT
)

var eventBits = []struct {
	Value Mask
	Name  string
}{
	{IN_ACCESS, "IN_ACCESS"},
	{IN_ATTRIB, "IN_ATTRIB"},
	{IN_CLOSE, "IN_CLOSE"},
	{IN_CLOSE_NOWRITE, "IN_CLOSE_NOWRITE"},
	{IN_CLOSE_WRITE, "IN_CLOSE_WRITE"},
	{IN_CREATE, "IN_CREATE"},
	{IN_DELETE, "IN_DELETE"},
	{IN_DELETE_SELF, "IN_DELETE_SELF"},
	{IN_MODIFY, "IN_MODIFY"},
	{IN_MOVE, "IN_MOVE"},
	{IN_MOVED_FROM, "IN_MOVED_FROM"},
	{IN_MOVED_TO, "IN_MOVED_TO"},
	{IN_MOVE_SELF, "IN_MOVE_SELF"},
	{IN_OPEN, "IN_OPEN"},
	{IN_ISDIR, "IN_ISDIR"},
	{IN_IGNORED, "IN_IGNORED"},
	{IN_Q_OVERFLOW, "IN_Q_OVERFLOW"},
	{IN_UNMOUNT, "IN_UNMOUNT"},
}
