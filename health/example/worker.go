package main

import (
	"fmt"
	"time"

	"ergo.services/actor/health"
	"ergo.services/ergo/act"
	"ergo.services/ergo/gen"
)

func WorkerFactory() gen.ProcessBehavior {
	return &Worker{}
}

type Worker struct {
	act.Actor
	heartbeatTimer gen.CancelFunc
}

type messageHeartbeatTick struct{}

func (w *Worker) Init(args ...any) error {
	// Register with the health actor
	err := health.Register(w, gen.Atom("health"), "db", health.ProbeLiveness|health.ProbeReadiness, 5*time.Second)
	if err != nil {
		return fmt.Errorf("failed to register health signal: %w", err)
	}

	// Start periodic heartbeat
	w.heartbeatTimer, _ = w.SendAfter(w.PID(), messageHeartbeatTick{}, 2*time.Second)

	w.Log().Info("worker initialized, registered 'db' signal with health actor")
	return nil
}

func (w *Worker) HandleMessage(from gen.PID, message any) error {
	switch message.(type) {
	case messageHeartbeatTick:
		health.Heartbeat(w, gen.Atom("health"), "db")
		w.heartbeatTimer, _ = w.SendAfter(w.PID(), messageHeartbeatTick{}, 2*time.Second)
	}
	return nil
}

func (w *Worker) Terminate(reason error) {
	if w.heartbeatTimer != nil {
		w.heartbeatTimer()
	}
	w.Log().Info("worker terminated: %s", reason)
}
