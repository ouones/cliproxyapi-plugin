package main

import (
	"testing"
	"time"
)

func TestPluginLifecycleQuiesceAndWaitDrainsActiveCall(t *testing.T) {
	state := newPluginLifecycleState()
	release, err := state.begin()
	if err != nil {
		t.Fatal(err)
	}

	finished := make(chan struct{})
	go func() {
		state.quiesceAndWait()
		close(finished)
	}()

	select {
	case <-finished:
		t.Fatal("quiesceAndWait returned before the active call was released")
	case <-time.After(10 * time.Millisecond):
	}

	release()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("quiesceAndWait did not return after the active call was released")
	}

	if _, err := state.begin(); err == nil {
		t.Fatal("new call was accepted after quiesceAndWait")
	}
	state.resume()
}
