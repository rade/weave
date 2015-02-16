package main

import (
	"fmt"
	"github.com/fsouza/go-dockerclient"
	lg "github.com/zettio/weave/common"
	"net"
	"strings"
)

type weaver struct {
	addr net.IP
	port int
}

func main() {
	makeWeaves(1)
}

func makeWeaves(n int) {
	namePrefix := "testweave"
	apiPath := "unix:/var/run/docker.sock"
	client, err := docker.NewClient(apiPath)
	checkError(err, apiPath)

	env, err := client.Version()
	checkError(err, apiPath)

	events := make(chan *docker.APIEvents)
	err = client.AddEventListener(events)
	checkError(err, apiPath)

	lg.Info.Printf("[updater] Using Docker API on %s: %v", apiPath, env)

	containers, _ := client.ListContainers(docker.ListContainersOptions{All: true})
	for _, cont := range containers {
		for _, name := range cont.Names {
			if strings.HasPrefix(name, "/"+namePrefix) {
				if strings.HasPrefix(cont.Status, "Up") {
					lg.Info.Println("Killing", name)
					checkFatal(client.KillContainer(docker.KillContainerOptions{ID: cont.ID}))
				}
				lg.Info.Println("Removing", cont.ID, cont.Names, cont.Status)
				checkFatal(client.RemoveContainer(docker.RemoveContainerOptions{ID: cont.ID}))
				break
			} else {
				lg.Info.Println("Ignoring", cont.ID, cont.Names, cont.Status)
			}
		}
	}

	config := &docker.Config{
		Image: "zettio/weave",
	}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("%s%d", namePrefix, i)
		opts := docker.CreateContainerOptions{Name: name, Config: config}
		lg.Info.Println("Creating", name)
		cont, err := client.CreateContainer(opts)
		checkFatal(err)
		cont.Args = []string{"-iface", "lo0"}
		err = client.StartContainer(cont.ID, nil)
		checkFatal(err)
	}
}

func checkError(err error, apiPath string) {
	if err != nil {
		lg.Error.Fatalf("Unable to connect to Docker API on %s: %s",
			apiPath, err)
	}
}

func checkFatal(err error) {
	if err != nil {
		lg.Error.Fatalf("Fatal error: %s", err)
	}
}
