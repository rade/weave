package main

import (
	"errors"
	"fmt"
	"github.com/docker/libcontainer/netlink"
	"github.com/docker/libcontainer/system"
	"github.com/fsouza/go-dockerclient"
	lg "github.com/zettio/weave/common"
	"net"
	"os"
	"runtime"
	"strings"
	"time"
)

type weaver struct {
	addr net.IP
	port int
}

func main() {
	makeWeaves(3)
}

type testContext struct {
	apiPath string
	dc      *docker.Client
}

const namePrefix = "testweave"

func makeWeaves(n int) {
	context := &testContext{apiPath: "unix:/var/run/docker.sock"}
	var err error
	context.dc, err = docker.NewClient(context.apiPath)
	check(err, context.apiPath)

	env, err := context.dc.Version()
	check(err, context.apiPath)

	events := make(chan *docker.APIEvents)
	err = context.dc.AddEventListener(events)
	check(err, context.apiPath)

	lg.Info.Printf("[updater] Using Docker API on %s: %v", context.apiPath, env)

	context.deleteOldContainers()

	// Give Docker and/or Linux time to react - if we don't sleep here
	// we get "Network interface already exists" from NetworkCreateVethPair
	time.Sleep(200 * time.Millisecond)

	// Start the Weave containers
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("%s%d", namePrefix, i)
		context.startOneWeave(name)
	}
}

// Delete any old containers created by this test prog
func (context *testContext) deleteOldContainers() {
	containers, _ := context.dc.ListContainers(docker.ListContainersOptions{All: true})
	for _, cont := range containers {
		for _, name := range cont.Names {
			if strings.HasPrefix(name, "/"+namePrefix) {
				if strings.HasPrefix(cont.Status, "Up") {
					lg.Info.Println("Killing", name)
					checkFatal(context.dc.KillContainer(docker.KillContainerOptions{ID: cont.ID}))
				}
				lg.Info.Println("Removing", cont.ID, cont.Names, cont.Status)
				checkFatal(context.dc.RemoveContainer(docker.RemoveContainerOptions{ID: cont.ID}))
				break
			}
		}
	}
}

func (context *testContext) startOneWeave(name string) *docker.Container {
	iface1, iface2 := createVeths(name)

	config := &docker.Config{
		Image: "zettio/weave",
		Cmd:   []string{"-iface", "ethwe", "-api", "none"},
	}
	opts := docker.CreateContainerOptions{Name: name, Config: config}
	lg.Info.Println("Creating", name)
	cont, err := context.dc.CreateContainer(opts)
	checkFatal(err)

	checkFatal(context.dc.StartContainer(cont.ID, nil))
	cont, err = context.dc.InspectContainer(cont.ID)
	checkFatal(err)
	if !cont.State.Running {
		lg.Error.Fatalf("Container %s (%s) exited immediately", name, cont.ID)
	}
	setupNetwork(cont, iface1, iface2)
	return cont
}

func createVeths(name string) (*net.Interface, *net.Interface) {
	vethname1, vethname2 := name+"x", name+"y"
	check(netlink.NetworkCreateVethPair(vethname1, vethname2, 42), "Creating veth pair")
	iface1, err := net.InterfaceByName(vethname1)
	check(err, "Finding interface")
	iface2, err := net.InterfaceByName(vethname2)
	check(err, "Finding interface")
	return iface1, iface2
}

func setupNetwork(cont *docker.Container, iface1, iface2 *net.Interface) {
	check(netlink.NetworkSetNsPid(iface2, cont.State.Pid), "Setting namespace")
	check(withNetnsPid(cont.State.Pid, func() error {
		if err := netlink.NetworkChangeName(iface2, "ethwe"); err != nil {
			return err
		}
		if err := netlink.NetworkLinkUp(iface2); err != nil {
			return err
		}
		return nil
	}), "with netns")
	check(netlink.NetworkLinkUp(iface1), "link up")
}

// equivalent of Weave script with_container_netns()
func withNetnsPid(pid int, f func() error) error {
	name := fmt.Sprintf("/proc/%d/ns/net", pid)
	if pidnsfile, err := os.Open(name); err != nil {
		return errors.New("Unable to open " + name + ": " + err.Error())
	} else if selfnsfile, err := os.Open("/proc/self/ns/net"); err != nil {
		return errors.New("Unable to open /proc/self/ns/net: " + err.Error())
	} else {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		system.Setns(pidnsfile.Fd(), 0)
		defer system.Setns(selfnsfile.Fd(), 0)
		return f()
	}
}

func checkFatal(err error) {
	if err != nil {
		lg.Error.Fatalf("Fatal error: %s", err)
	}
}

func check(err error, desc string) {
	if err != nil {
		lg.Error.Fatalf("%s: %s", desc, err)
	}
}
