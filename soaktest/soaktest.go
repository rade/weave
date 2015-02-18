package main

import (
	"fmt"
	"github.com/fsouza/go-dockerclient"
	lg "github.com/zettio/weave/common"
	wt "github.com/zettio/weave/testing"
	"github.com/zettio/weave/weaveapi"
	"math/rand"
	"net"
	"strings"
	"time"
)

type weaver struct {
	addr net.IP
	port int
}

func main() {
	//RunWithTimeout(10*time.Second, TestAllocFromOne)
	//RunWithTimeout(20*time.Second, TestAllocFromRand)
	TestCreationAndDestruction()
}

// Borrowed from net/http tests:
// goTimeout runs f, failing t if f takes more than d to complete.
func RunWithTimeout(d time.Duration, f func()) {
	ch := make(chan bool, 2)
	timer := time.AfterFunc(d, func() {
		lg.Error.Printf("Timeout expired after %v: stacks:\n%s", d, wt.StackTraceAll())
		ch <- true
	})
	defer timer.Stop()
	go func() {
		defer func() { ch <- true }()
		f()
	}()
	<-ch
}

func TestCreationAndDestruction() {
	N := 12
	context := &testContext{apiPath: "unix:/var/run/docker.sock"}
	context.init(N)
	context.makeWeaves(N)
}

func TestAllocFromOne() {
	N := 3
	context := &testContext{apiPath: "unix:/var/run/docker.sock"}
	context.init(N)
	context.makeWeaves(N)
	for i := 0; ; i++ {
		_, err := context.clients[0].AllocateIPFor("foobar")
		if err != nil {
			lg.Info.Printf("Managed to allocate %d addresses\n", i)
			break
		}
	}
}

func TestAllocFromRand() {
	N := 5
	ips := make(map[string]int)
	context := &testContext{apiPath: "unix:/var/run/docker.sock"}
	context.init(N)
	context.makeWeaves(N)
	for i := 0; ; i++ {
		client := rand.Intn(N)
		lg.Info.Printf("%d: Calling %d\n", i, client)
		ip, err := context.clients[client].AllocateIPFor("foobar")
		if err != nil {
			lg.Info.Printf("Managed to allocate %d addresses\n", i)
			break
		}
		if prevc, found := ips[ip]; found {
			lg.Error.Fatalf("IP address %s already allocated from client %d", ip, prevc)
		}
		ips[ip] = client
	}
}

type testContext struct {
	apiPath string
	dc      *docker.Client
	conts   []*docker.Container
	clients []*weaveapi.Client
}

const namePrefix = "testweave"

func (context *testContext) init(n int) {
	var err error
	context.dc, err = docker.NewClient(context.apiPath)
	context.checkFatal(err, context.apiPath)

	env, err := context.dc.Version()
	context.checkFatal(err, context.apiPath)

	events := make(chan *docker.APIEvents)
	err = context.dc.AddEventListener(events)
	context.checkFatal(err, context.apiPath)

	lg.Info.Printf("[updater] Using Docker API on %s: %v", context.apiPath, env)

	context.conts = make([]*docker.Container, n)
	context.clients = make([]*weaveapi.Client, n)
}

func (context *testContext) makeWeaves(n int) {
	context.deleteOldContainers()

	// Give Docker and/or Linux time to react - if we don't sleep here
	// we get "Network interface already exists" from NetworkCreateVethPair
	time.Sleep(time.Duration(100*n) * time.Millisecond)

	// Start the Weave containers
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("%s%d", namePrefix, i)
		context.conts[i] = context.startOneWeave(name)
		if context.conts[i] == nil {
			lg.Error.Fatalf("Fatal error: when creating Weaves")
		}
		net := context.conts[i].NetworkSettings
		context.clients[i] = weaveapi.NewClient(net.IPAddress)
	}

	// Give the Weaves time to start up
	time.Sleep(time.Duration(200*n) * time.Millisecond)

	// Make some connections (note Docker assumed to be running with --icc=true)
	for i := 0; i < n-1; i++ {
		context.check(context.clients[i].Connect(context.conts[i+1].NetworkSettings.IPAddress), "connect")
	}

	// Give the connections time to settle
	time.Sleep(time.Duration(100*n) * time.Millisecond)
}

func (context *testContext) addWeave(i int) {
	name := fmt.Sprintf("%s%d", namePrefix, i)
	context.conts[i] = context.startOneWeave(name)
	net := context.conts[i].NetworkSettings
	context.clients[i] = weaveapi.NewClient(net.IPAddress)

	// Give the Weave time to start up
	time.Sleep(time.Duration(200) * time.Millisecond)
}

func (context *testContext) connectWeave(i int) {
	for pos, cont := range context.conts {
		if pos != i && cont.State.Running {
			context.check(context.clients[i].Connect(cont.NetworkSettings.IPAddress), "connect")
			break
		}
	}
	// Give the connection time to settle
	time.Sleep(time.Duration(100) * time.Millisecond)
}

func (c *testContext) killWeave(i int) {
	cont := c.conts[i]
	c.check(c.dc.KillContainer(docker.KillContainerOptions{ID: cont.ID}), "kill container")
	c.check(c.dc.RemoveContainer(docker.RemoveContainerOptions{ID: cont.ID}), "remove container")
	cont.State.Running = false
}

// Delete any old containers created by this test prog
func (context *testContext) deleteOldContainers() {
	containers, _ := context.dc.ListContainers(docker.ListContainersOptions{All: true})
	for _, cont := range containers {
		for _, name := range cont.Names {
			if strings.HasPrefix(name, "/"+namePrefix) {
				if strings.HasPrefix(cont.Status, "Up") {
					lg.Info.Println("Killing", name)
					context.checkFatal(context.dc.KillContainer(docker.KillContainerOptions{ID: cont.ID}), "kill container")
				}
				lg.Info.Println("Removing", cont.ID, cont.Names, cont.Status)
				context.checkFatal(context.dc.RemoveContainer(docker.RemoveContainerOptions{ID: cont.ID}), "removing container")
				break
			}
		}
	}
}

func (context *testContext) startOneWeave(name string) *docker.Container {
	iface1, iface2, err := createVeths(name)
	context.check(err, "Creating veth pair")

	config := &docker.Config{
		Image: "zettio/weave",
		Cmd:   []string{"-iface", "ethwe"}, //, "-api", "none",
		//"-autoAddConnections=false",
		//"-alloc", "10.0.0.0/22", "-debug"},
	}
	opts := docker.CreateContainerOptions{Name: name, Config: config}
	lg.Info.Println("Creating", name)
	cont, err := context.dc.CreateContainer(opts)
	if err != nil {
		lg.Error.Printf("Error when creating container: %s\n", err)
		return nil
	}

	err = context.dc.StartContainer(cont.ID, nil)
	if err != nil {
		lg.Error.Printf("Error when starting container: %s\n", err)
		return nil
	}
	cont, err = context.dc.InspectContainer(cont.ID)
	if !cont.State.Running {
		lg.Error.Printf("Container %s (%s) exited immediately", name, cont.ID)
	}
	context.check(setupNetwork(cont.State.Pid, iface1, iface2), "setup network")
	return cont
}

func (c *testContext) checkFatal(err error, desc string) {
	if err != nil {
		lg.Error.Fatalf("Fatal error: %s: %s", desc, err)
	}
}

func (c *testContext) check(err error, desc string) {
	if err != nil {
		lg.Error.Printf("%s: %s", desc, err)
	}
}
