package main

import (
	"errors"
	"fmt"
	"github.com/docker/libcontainer/netlink"
	"github.com/docker/libcontainer/system"
	"net"
	"os"
	"runtime"
)

func createVeths(pid int) (*net.Interface, *net.Interface, error) {
	pstr := fmt.Sprintf("%d", pid)
	vethname1, vethname2 := namePrefix+pstr+"x", namePrefix+pstr+"y"
	if err := netlink.NetworkCreateVethPair(vethname1, vethname2, 42); err != nil {
		return nil, nil, err
	}
	iface1, err := net.InterfaceByName(vethname1)
	if err != nil {
		return nil, nil, err
	}
	iface2, err := net.InterfaceByName(vethname2)
	if err != nil {
		return nil, nil, err
	}
	return iface1, iface2, nil
}

func setupNetwork(pid int, iface1, iface2 *net.Interface) error {
	if err := netlink.NetworkSetNsPid(iface2, pid); err != nil {
		return errors.New("NetworkSetNsPid: " + err.Error())
	}
	err := withNetnsPid(pid, func() error {
		if err := netlink.NetworkChangeName(iface2, "ethwe"); err != nil {
			return err
		}
		if err := netlink.NetworkLinkUp(iface2); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := netlink.NetworkLinkUp(iface1); err != nil {
		return errors.New("NetworkLinkUp: " + err.Error())
	}
	return nil
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
