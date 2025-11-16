package main

import (
	"flag"

	"ergo.services/actor/metrics"
	"ergo.services/application/observer"
	"ergo.services/ergo"
	"ergo.services/ergo/gen"
)

func main() {
	// Default metrics factory to provide standard metrics
	factory := metrics.Factory

	custom := flag.Bool("custom", false, "Use custom metrics with user-defined metrics")
	flag.Parse()

	// Start the node with Observer application
	nodeName := gen.Atom("metrics-demo@localhost")

	n, err := ergo.StartNode(nodeName, gen.NodeOptions{
		Applications: []gen.ApplicationBehavior{
			observer.CreateApp(observer.Options{
				Port: 9911,
				Host: "localhost",
			}),
		},
	})
	if err != nil {
		panic(err)
	}
	defer n.Stop()

	n.Log().Info("Observer application started at: http://localhost:9911")

	// Extend default metrics with custom ones if -custom flag is set
	if *custom {
		factory = CustomFactory
	}

	// Spawn metrics actor
	_, err = n.Spawn(factory, gen.ProcessOptions{})
	if err != nil {
		panic(err)
	}

	n.Log().Info("Metrics available at: http://localhost:3000/metrics")
	n.Log().Info("")
	n.Log().Info("Run with -custom flag to see custom metrics:")
	n.Log().Info("  ./example -custom")
	n.Log().Info("")
	n.Log().Info("Press Ctrl+C to stop...")

	n.Wait()
}
