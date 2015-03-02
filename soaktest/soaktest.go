package main

import (
	"fmt"
	"github.com/fsouza/go-dockerclient"
	lg "github.com/zettio/weave/common"
	wt "github.com/zettio/weave/testing"
	"github.com/zettio/weave/weaveapi"
	"math/rand"
	"net"
	"os"
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
	//TestCreationAndDestruction()
	TestAllocAndDelete()
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
	N := 22
	longRunning := 11
	context := &testContext{apiPath: "unix:/var/run/docker.sock"}
	context.init(N)
	context.makeWeaves(N)
	for {
		oper := rand.Intn(2)
		switch oper {
		case 0: // kill a weave
			i := rand.Intn(N-longRunning) + longRunning
			if context.conts[i] != nil && context.conts[i].State.Running {
				context.killWeave(context.conts[i])
			}
		case 1: // start a weave
			i := rand.Intn(N-longRunning) + longRunning
			if context.conts[i] == nil || !context.conts[i].State.Running {
				context.makeWeave(i)
				time.Sleep(time.Duration(500) * time.Millisecond)
				context.connectWeave(i)
			}
		}
		time.Sleep(time.Second)
	}
}

func TestAllocFromOne() {
	N := 3
	context := &testContext{apiPath: "unix:/var/run/docker.sock"}
	context.init(N)
	context.makeWeaves(N, "-alloc", "10.0.0.0/22")
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
	context.makeWeaves(N, "-alloc", "10.0.0.0/22")
	for i := 0; ; i++ {
		client := rand.Intn(N)
		lg.Info.Printf("%d: Calling %d\n", i, client)
		ip, err := context.clients[client].AllocateIPFor("foobar")
		if err != nil {
			lg.Info.Printf("Managed to allocate %d addresses\n", i)
			break
		}
		if prevc, found := ips[ip]; found {
			lg.Error.Fatalf("IP address %s already allocated from weave %d", ip, prevc)
		}
		ips[ip] = client
	}
}

func TestAllocAndDelete() {
	N := 5
	const MaxAddresses = 1022 // based on /22 address range
	ips := make([]string, MaxAddresses)
	ipmap := make(map[string]int)

	context := &testContext{apiPath: "unix:/var/run/docker.sock"}
	context.init(N)
	context.makeWeaves(N, "-alloc", "10.0.0.0/22")

	allocate := func(client, n int) {
		ident := fmt.Sprintf("client%d", n)
		cidr, err := context.clients[client].AllocateIPFor(ident)
		part := strings.Split(cidr, "/")
		if err != nil {
			lg.Info.Printf("Error with %d addresses allocated: %s", n, err)
		} else if prevc, found := ipmap[part[0]]; found {
			lg.Error.Fatalf("IP address %s returned from weave %d already allocated from weave %d", part[0], client, prevc)
		} else {
			lg.Info.Printf("Allocated %s for %s from %d\n", cidr, ident, client)
			ips[n] = part[0]
			ipmap[part[0]] = client
		}
	}

	for i := 0; i < MaxAddresses*9/10; i++ {
		client := rand.Intn(N)
		allocate(client, i)
	}
	for {
		oper := rand.Intn(2)
		switch oper {
		case 0: // free
			n := rand.Intn(MaxAddresses)
			ip := ips[n]
			if ip == "" {
				break
			}
			client := ipmap[ip]
			ident := fmt.Sprintf("client%d", n)
			lg.Info.Printf("Freeing %s/%s from %d", ident, ip, client)
			_, err := context.clients[client].FreeIPFor(ip, ident)
			if err != nil {
				lg.Error.Fatalf("Error on freeing %s: %s", ip, err)
			} else {
				delete(ipmap, ip)
				ips[n] = ""
			}
		case 1: // allocate multiple addresses from the same peer
			client := rand.Intn(N)
			num := rand.Intn(100)
			for i := 0; i < num; i++ {
				n := rand.Intn(MaxAddresses)
				if ips[n] != "" {
					continue
				}
				allocate(client, n)
			}
		}
		time.Sleep(time.Second / 10)
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

func (context *testContext) makeWeaves(n int, args ...string) {
	context.deleteOldContainers()

	// Give Docker and/or Linux time to react - if we don't sleep here
	// we get "Network interface already exists" from NetworkCreateVethPair
	time.Sleep(time.Duration(100*n) * time.Millisecond)

	// Start the Weave containers
	for i := 0; i < n; i++ {
		context.makeWeave(i, args...)
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

func (context *testContext) makeWeave(i int, args ...string) {
	name := fmt.Sprintf("%s%d", namePrefix, i)
	context.conts[i] = context.startOneWeave(name, args...)
	if context.conts[i] == nil {
		lg.Error.Printf("Fatal error: when creating Weaves")
		return
	}
	net := context.conts[i].NetworkSettings
	context.clients[i] = weaveapi.NewClient(net.IPAddress)
}

func (context *testContext) connectWeave(i int) {
	const MaxRetries = 5
	for pos, cont := range context.conts {
		if pos != i && cont.State.Running {
			var err error
			for count := 0; count < MaxRetries; count++ {
				err = context.clients[i].Connect(cont.NetworkSettings.IPAddress)
				if err == nil {
					break
				}
				time.Sleep(time.Second)
			}
			context.check(err, "connect")
			break
		}
	}
	// Give the connection time to settle
	time.Sleep(time.Duration(100) * time.Millisecond)
}

func (c *testContext) killWeave(cont *docker.Container) {
	lg.Info.Println("Killing weave", cont.Name)
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

func (context *testContext) startOneWeave(name string, args ...string) *docker.Container {
	config := &docker.Config{
		Image: "zettio/weave",
		Cmd:   []string{"-iface", "ethwe", "--nickname", name}, //, "-api", "none",
		//"-autoAddConnections=false",
		//"-alloc", "10.0.0.0/22", "-debug"},
	}
	config.Cmd = append(config.Cmd, os.Args[1:]...)
	config.Cmd = append(config.Cmd, args...)
	opts := docker.CreateContainerOptions{Name: name, Config: config}
	lg.Info.Println("Creating", name)
	cont, err := context.dc.CreateContainer(opts)
	if err != nil {
		lg.Error.Printf("Error when creating container: %s\n", err)
		return nil
	}

	hostConfig := &docker.HostConfig{
		Binds: []string{"/var/run/docker.sock:/var/run/docker.sock"},
	}
	err = context.dc.StartContainer(cont.ID, hostConfig)
	if err != nil {
		lg.Error.Printf("Error when starting container: %s\n", err)
		return nil
	}
	cont, err = context.dc.InspectContainer(cont.ID)
	if !cont.State.Running {
		lg.Error.Printf("Container %s (%s) exited immediately", name, cont.ID)
	}

	iface1, iface2, err := createVeths(cont.State.Pid)
	if err != nil {
		lg.Error.Printf("Error when creating veth pair %s: %s\n", name, err)
		return nil
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
