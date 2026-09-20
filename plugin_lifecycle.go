package main

import (
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

type pluginLifecycleState struct {
	mu        sync.Mutex
	cond      *sync.Cond
	active    int
	quiescing bool
}

func newPluginLifecycleState() *pluginLifecycleState {
	state := &pluginLifecycleState{}
	state.cond = sync.NewCond(&state.mu)
	return state
}

var commandCodePluginLifecycle = newPluginLifecycleState()

func (s *pluginLifecycleState) begin() (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.quiescing {
		return nil, pluginabi.NewError("plugin_quiescing", "Command Code plugin is quiescing")
	}
	s.active++

	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.active--
			if s.active == 0 {
				s.cond.Broadcast()
			}
			s.mu.Unlock()
		})
	}, nil
}

func (s *pluginLifecycleState) quiesce() {
	s.mu.Lock()
	s.quiescing = true
	s.mu.Unlock()
}

func (s *pluginLifecycleState) resume() {
	s.mu.Lock()
	s.quiescing = false
	s.mu.Unlock()
}

func (s *pluginLifecycleState) quiesceAndWait() {
	s.quiesce()
	s.mu.Lock()
	for s.active > 0 {
		s.cond.Wait()
	}
	s.mu.Unlock()
}
