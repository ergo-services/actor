package main

import (
	"ergo.services/actor/health"
	"ergo.services/ergo"
	"ergo.services/ergo/gen"
)

func main() {
	nodeName := gen.Atom("health-demo@localhost")

	n, err := ergo.StartNode(nodeName, gen.NodeOptions{})
	if err != nil {
		panic(err)
	}
	defer n.Stop()

	// Spawn health actor on port 3001
	_, err = n.SpawnRegister(gen.Atom("health"), health.Factory, gen.ProcessOptions{},
		health.Options{Port: 3001})
	if err != nil {
		panic(err)
	}
	n.Log().Info("health endpoints available at http://localhost:3001/health/*")

	// Spawn demo worker that registers a signal and sends heartbeats
	_, err = n.Spawn(WorkerFactory, gen.ProcessOptions{})
	if err != nil {
		panic(err)
	}

	n.Log().Info("demo worker started, sending heartbeats every 2s (timeout 5s)")
	n.Log().Info("press Ctrl+C to stop")

	n.Wait()
}
