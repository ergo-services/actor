package main

import (
	"fmt"
	"time"

	"ergo.services/actor/leader"
	"ergo.services/ergo"
	"ergo.services/ergo/gen"
)

// MyService embeds leader.Actor
type MyService struct {
	leader.Actor
	name string
}

func (s *MyService) Init(args ...any) (leader.Options, error) {
	// Get configuration from args
	s.name = args[0].(string)
	clusterID := args[1].(string)
	bootstrap := args[2].([]gen.ProcessID)

	// Return Options for leader election
	return leader.Options{
		ClusterID: clusterID,
		Bootstrap: bootstrap,
	}, nil
}

// Override leader callbacks
func (s *MyService) HandleBecomeLeader() {
	fmt.Printf("[%s] I AM LEADER!\n", s.name)
}

func (s *MyService) HandleBecomeFollower(leader gen.PID) {
	if leader == (gen.PID{}) {
		fmt.Printf("[%s] No leader\n", s.name)
	} else {
		fmt.Printf("[%s] Following: %s\n", s.name, leader)
	}
}

func main() {
	fmt.Println("=== Simple Leader Election Example ===")

	clusterID := "my-cluster"
	procName := gen.Atom("service")

	// 3 node cluster
	names := []string{"node1@localhost", "node2@localhost", "node3@localhost"}

	bootstrap := make([]gen.ProcessID, len(names))
	for i, name := range names {
		bootstrap[i] = gen.ProcessID{Name: procName, Node: gen.Atom(name)}
	}

	// Start nodes
	fmt.Println("Starting 3-node cluster...")
	nodes := make([]gen.Node, 3)

	for i := 0; i < 3; i++ {
		opts := gen.NodeOptions{
			Network: gen.NetworkOptions{Cookie: "test"},
		}

		n, _ := ergo.StartNode(gen.Atom(names[i]), opts)
		nodes[i] = n

		_, _ = n.SpawnRegister(procName, func() gen.ProcessBehavior {
			return &MyService{}
		}, gen.ProcessOptions{}, names[i], clusterID, bootstrap)
	}

	time.Sleep(500 * time.Millisecond)
	fmt.Println("Leader elected")

	// Kill leader
	fmt.Println("Killing node1 (likely leader)...")
	nodes[0].Stop()
	time.Sleep(500 * time.Millisecond)

	fmt.Println("New leader elected")

	// Cleanup
	for _, n := range nodes {
		if n != nil {
			n.Stop()
		}
	}

	fmt.Println("Done!")
}
