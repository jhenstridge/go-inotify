// Copyright 2010 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux
// +build linux

package inotify

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

type eventCollector struct {
	watcher  *Watcher
	expected int
	events   []Event
	done     chan struct{}
}

func collectEvents(watcher *Watcher, expected int) *eventCollector {
	c := &eventCollector{
		watcher:  watcher,
		expected: expected,
		done:     make(chan struct{}),
	}
	go c.watch()
	return c
}

func (c *eventCollector) watch() {
	defer func() { close(c.done) }()

	timer := time.NewTimer(1 * time.Second)
	defer timer.Stop()

	for {
		select {
		case e, ok := <-c.watcher.Event:
			if !ok {
				return
			}
			c.events = append(c.events, e)
			if len(c.events) >= c.expected {
				return
			}
		case <-timer.C:
			return
		}
	}
}

func (c *eventCollector) expect(t *testing.T, expected []Event) {
	t.Helper()
	<-c.done
	if len(expected) != len(c.events) {
		t.Fatalf("expected %d events, got %d events", len(expected), len(c.events))
	}
	for i := range c.events {
		if expected[i] != c.events[i] {
			t.Fatalf("Event %d differs:\n  expected: %s\n  actual: %s", i, expected[i], c.events[i])
		}
	}
}

func TestInotifyEvents(t *testing.T) {
	// Create an inotify watcher instance and initialize it
	watcher, err := NewWatcher()
	if err != nil {
		t.Fatalf("NewWatcher failed: %s", err)
	}

	dir := t.TempDir()

	// Add a watch for "_test"
	watch, err := watcher.Watch(dir)
	if err != nil {
		t.Fatalf("Watch failed: %s", err)
	}
	if watch == nil {
		t.Fatalf("Watch returned a nil watch")
	}

	const testFile = "TestInotifyEvents.testfile"

	events := collectEvents(watcher, 3)

	// Create a file
	// This should add at least one event to the inotify event queue
	f, err := os.OpenFile(filepath.Join(dir, testFile), os.O_WRONLY|os.O_CREATE, 0o666)
	if err != nil {
		t.Fatalf("creating test file: %s", err)
	}
	f.Close()

	events.expect(t, []Event{
		{watch, IN_CREATE, 0, testFile},
		{watch, IN_OPEN, 0, testFile},
		{watch, IN_CLOSE_WRITE, 0, testFile},
	})

	// Try closing the inotify instance
	t.Log("calling Close()")
	err = watcher.Close()
	if err != nil {
		t.Fatalf("error closing watcher: %s", err)
	}
	t.Log("waiting for the event channel to become closed...")
	select {
	case <-watcher.Event:
		t.Log("event channel closed")
	case <-time.After(1 * time.Second):
		t.Fatal("event stream was not closed after 1 second")
	}
}

func TestInotifyClose(t *testing.T) {
	watcher, _ := NewWatcher()
	err := watcher.Close()
	if err != nil {
		t.Fatalf("error closing watcher: %s", err)
	}

	done := make(chan bool)
	go func() {
		watcher.Close()
		done <- true
	}()

	select {
	case <-done:
	case <-time.After(50 * time.Millisecond):
		t.Fatal("double Close() test failed: second Close() call didn't return")
	}

	dir := t.TempDir()
	_, err = watcher.Watch(dir)
	if err == nil {
		t.Fatal("expected error on Watch() after Close(), got nil")
	}
}

func TestInotifyMaskCreate(t *testing.T) {
	watcher, err := NewWatcher()
	if err != nil {
		t.Fatalf("error creating watcher: %s", err)
	}
	defer watcher.Close()

	dir := t.TempDir()
	_, err = watcher.AddWatch(dir, IN_CLOSE_WRITE)
	if err != nil {
		t.Fatalf("error creating watch: %s", err)
	}

	_, err = watcher.AddWatch(dir, IN_MASK_CREATE|IN_OPEN)
	if !errors.Is(err, syscall.EEXIST) {
		t.Fatalf("expected EEXIST error, got: %s", err)
	}
}

func TestInotifyMaskAdd(t *testing.T) {
	watcher, err := NewWatcher()
	if err != nil {
		t.Fatalf("error creating watcher: %s", err)
	}
	defer watcher.Close()
	events := collectEvents(watcher, 2)

	dir := t.TempDir()
	watch1, err := watcher.AddWatch(dir, IN_OPEN)
	if err != nil {
		t.Fatalf("error creating watch: %s", err)
	}
	watch2, err := watcher.AddWatch(dir, IN_MASK_ADD|IN_CLOSE_WRITE)
	if err != nil {
		t.Fatalf("error creating watch: %s", err)
	}
	if watch1 != watch2 {
		t.Fatalf("AddWatch calls returned different watches: %#v != %#v", watch1, watch2)
	}

	const testFile = "TestInotifyEvents.testfile"
	f, err := os.OpenFile(filepath.Join(dir, testFile), os.O_WRONLY|os.O_CREATE, 0o666)
	if err != nil {
		t.Fatalf("creating test file: %s", err)
	}
	f.Close()

	// Both events are delivered
	events.expect(t, []Event{
		{watch1, IN_OPEN, 0, testFile},
		{watch1, IN_CLOSE_WRITE, 0, testFile},
	})
}

func TestInotifyMaskReplace(t *testing.T) {
	watcher, err := NewWatcher()
	if err != nil {
		t.Fatalf("error creating watcher: %s", err)
	}
	defer watcher.Close()
	events := collectEvents(watcher, 2)

	dir := t.TempDir()
	watch1, err := watcher.AddWatch(dir, IN_OPEN)
	if err != nil {
		t.Fatalf("error creating watch: %s", err)
	}
	watch2, err := watcher.AddWatch(dir, IN_CREATE|IN_CLOSE_WRITE)
	if err != nil {
		t.Fatalf("error creating watch: %s", err)
	}
	if watch1 != watch2 {
		t.Fatalf("AddWatch calls returned different watches: %#v != %#v", watch1, watch2)
	}

	const testFile = "TestInotifyEvents.testfile"
	f, err := os.OpenFile(filepath.Join(dir, testFile), os.O_WRONLY|os.O_CREATE, 0o666)
	if err != nil {
		t.Fatalf("creating test file: %s", err)
	}
	f.Close()

	// The IN_OPEN event is not delivered, since the mask was replaced
	events.expect(t, []Event{
		{watch1, IN_CREATE, 0, testFile},
		{watch1, IN_CLOSE_WRITE, 0, testFile},
	})
}

func TestInotifyWatchRemovedOnDelete(t *testing.T) {
	watcher, err := NewWatcher()
	if err != nil {
		t.Fatalf("error creating watcher: %s", err)
	}
	defer watcher.Close()
	events := collectEvents(watcher, 2)

	dir := t.TempDir()
	subdir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatalf("error creating subdir: %s", err)
	}
	watch, err := watcher.AddWatch(subdir, IN_DELETE_SELF)
	if err != nil {
		t.Fatalf("error creating watch: %s", err)
	}

	if err := os.Remove(subdir); err != nil {
		t.Fatalf("error removing subdir: %s", err)
	}
	events.expect(t, []Event{
		{watch, IN_DELETE_SELF, 0, ""},
		{watch, IN_IGNORED, 0, ""},
	})
	// The watcher has now forgotten about the watch
	if len(watcher.watches) != 0 {
		t.Fatalf("watch map is not empty: %#v", watcher.watches)
	}
}
