// +build !windows

package main

import (
	"bufio"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode"

	"github.com/docker/docker/pkg/integration/checker"
	"github.com/go-check/check"
	"github.com/kr/pty"
)

// #5979
func (s *DockerSuite) TestEventsRedirectStdout(c *check.C) {
	since := daemonTime(c).Unix()
	dockerCmd(c, "run", "busybox", "true")

	file, err := ioutil.TempFile("", "")
	c.Assert(err, checker.IsNil, check.Commentf("could not create temp file"))
	defer os.Remove(file.Name())

	command := fmt.Sprintf("%s events --since=%d --until=%d > %s", dockerBinary, since, daemonTime(c).Unix(), file.Name())
	_, tty, err := pty.Open()
	c.Assert(err, checker.IsNil, check.Commentf("Could not open pty"))
	cmd := exec.Command("sh", "-c", command)
	cmd.Stdin = tty
	cmd.Stdout = tty
	cmd.Stderr = tty
	c.Assert(cmd.Run(), checker.IsNil, check.Commentf("run err for command %q", command))

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		for _, ch := range scanner.Text() {
			c.Assert(unicode.IsControl(ch), checker.False, check.Commentf("found control character %v", []byte(string(ch))))
		}
	}
	c.Assert(scanner.Err(), checker.IsNil, check.Commentf("Scan err for command %q", command))

}

func (s *DockerSuite) TestEventsOOMDisableFalse(c *check.C) {
	testRequires(c, DaemonIsLinux, oomControl, memoryLimitSupport, NotGCCGO)

	errChan := make(chan error)
	go func() {
		defer close(errChan)
		out, exitCode, _ := dockerCmdWithError("run", "--name", "oomFalse", "-m", "10MB", "busybox", "sh", "-c", "x=a; while true; do x=$x$x$x$x; done")
		if expected := 137; exitCode != expected {
			errChan <- fmt.Errorf("wrong exit code for OOM container: expected %d, got %d (output: %q)", expected, exitCode, out)
		}
	}()
	select {
	case err := <-errChan:
		c.Assert(err, checker.IsNil)
	case <-time.After(30 * time.Second):
		c.Fatal("Timeout waiting for container to die on OOM")
	}

	out, _ := dockerCmd(c, "events", "--since=0", "-f", "container=oomFalse", fmt.Sprintf("--until=%d", daemonTime(c).Unix()))
	events := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	nEvents := len(events)

	c.Assert(nEvents, checker.GreaterOrEqualThan, 5) //Missing expected event
	c.Assert(parseEventAction(c, events[nEvents-5]), checker.Equals, "create")
	c.Assert(parseEventAction(c, events[nEvents-4]), checker.Equals, "attach")
	c.Assert(parseEventAction(c, events[nEvents-3]), checker.Equals, "start")
	c.Assert(parseEventAction(c, events[nEvents-2]), checker.Equals, "oom")
	c.Assert(parseEventAction(c, events[nEvents-1]), checker.Equals, "die")
}

func (s *DockerSuite) TestEventsOOMDisableTrue(c *check.C) {
	testRequires(c, DaemonIsLinux, oomControl, memoryLimitSupport, NotGCCGO, NotArm)

	errChan := make(chan error)
	observer, err := newEventObserver(c)
	c.Assert(err, checker.IsNil)
	err = observer.Start()
	c.Assert(err, checker.IsNil)
	defer observer.Stop()

	go func() {
		defer close(errChan)
		out, exitCode, _ := dockerCmdWithError("run", "--oom-kill-disable=true", "--name", "oomTrue", "-m", "10MB", "busybox", "sh", "-c", "x=a; while true; do x=$x$x$x$x; done")
		if expected := 137; exitCode != expected {
			errChan <- fmt.Errorf("wrong exit code for OOM container: expected %d, got %d (output: %q)", expected, exitCode, out)
		}
	}()

	c.Assert(waitRun("oomTrue"), checker.IsNil)
	defer dockerCmd(c, "kill", "oomTrue")
	containerID := inspectField(c, "oomTrue", "Id")

	testActions := map[string]chan bool{
		"oom": make(chan bool),
	}

	matcher := matchEventLine(containerID, "container", testActions)
	processor := processEventMatch(testActions)
	go observer.Match(matcher, processor)

	select {
	case <-time.After(20 * time.Second):
		observer.CheckEventError(c, containerID, "oom", matcher)
	case <-testActions["oom"]:
		// ignore, done
	case errRun := <-errChan:
		if errRun != nil {
			c.Fatalf("%v", errRun)
		} else {
			c.Fatalf("container should be still running but it's not")
		}
	}

	status := inspectField(c, "oomTrue", "State.Status")
	c.Assert(strings.TrimSpace(status), checker.Equals, "running", check.Commentf("container should be still running"))
}

// #18453
func (s *DockerSuite) TestEventsContainerFilterByName(c *check.C) {
	testRequires(c, DaemonIsLinux)
	cOut, _ := dockerCmd(c, "run", "--name=foo", "-d", "busybox", "top")
	c1 := strings.TrimSpace(cOut)
	waitRun("foo")
	cOut, _ = dockerCmd(c, "run", "--name=bar", "-d", "busybox", "top")
	c2 := strings.TrimSpace(cOut)
	waitRun("bar")
	out, _ := dockerCmd(c, "events", "-f", "container=foo", "--since=0", fmt.Sprintf("--until=%d", daemonTime(c).Unix()))
	c.Assert(out, checker.Contains, c1, check.Commentf(out))
	c.Assert(out, checker.Not(checker.Contains), c2, check.Commentf(out))
}

// #18453
func (s *DockerSuite) TestEventsContainerFilterBeforeCreate(c *check.C) {
	testRequires(c, DaemonIsLinux)
	var (
		out string
		ch  chan struct{}
	)
	ch = make(chan struct{})

	// calculate the time it takes to create and start a container and sleep 2 seconds
	// this is to make sure the docker event will recevie the event of container
	since := daemonTime(c).Unix()
	id, _ := dockerCmd(c, "run", "-d", "busybox", "top")
	cID := strings.TrimSpace(id)
	waitRun(cID)
	time.Sleep(2 * time.Second)
	duration := daemonTime(c).Unix() - since

	go func() {
		out, _ = dockerCmd(c, "events", "-f", "container=foo", "--since=0", fmt.Sprintf("--until=%d", daemonTime(c).Unix()+2*duration))
		close(ch)
	}()
	// Sleep 2 second to wait docker event to start
	time.Sleep(2 * time.Second)
	id, _ = dockerCmd(c, "run", "--name=foo", "-d", "busybox", "top")
	cID = strings.TrimSpace(id)
	waitRun(cID)
	<-ch
	c.Assert(out, checker.Contains, cID, check.Commentf("Missing event of container (foo)"))
}

func (s *DockerSuite) TestVolumeEvents(c *check.C) {
	testRequires(c, DaemonIsLinux)

	since := daemonTime(c).Unix()

	// Observe create/mount volume actions
	dockerCmd(c, "volume", "create", "--name", "test-event-volume-local")
	dockerCmd(c, "run", "--name", "test-volume-container", "--volume", "test-event-volume-local:/foo", "-d", "busybox", "true")
	waitRun("test-volume-container")

	// Observe unmount/destroy volume actions
	dockerCmd(c, "rm", "-f", "test-volume-container")
	dockerCmd(c, "volume", "rm", "test-event-volume-local")

	out, _ := dockerCmd(c, "events", fmt.Sprintf("--since=%d", since), fmt.Sprintf("--until=%d", daemonTime(c).Unix()))
	events := strings.Split(strings.TrimSpace(out), "\n")
	c.Assert(len(events), checker.GreaterThan, 4)

	volumeEvents := eventActionsByIDAndType(c, events, "test-event-volume-local", "volume")
	c.Assert(volumeEvents, checker.HasLen, 4)
	c.Assert(volumeEvents[0], checker.Equals, "create")
	c.Assert(volumeEvents[1], checker.Equals, "mount")
	c.Assert(volumeEvents[2], checker.Equals, "unmount")
	c.Assert(volumeEvents[3], checker.Equals, "destroy")
}

func (s *DockerSuite) TestNetworkEvents(c *check.C) {
	testRequires(c, DaemonIsLinux)

	since := daemonTime(c).Unix()

	// Observe create/connect network actions
	dockerCmd(c, "network", "create", "test-event-network-local")
	dockerCmd(c, "run", "--name", "test-network-container", "--net", "test-event-network-local", "-d", "busybox", "true")
	waitRun("test-network-container")

	// Observe disconnect/destroy network actions
	dockerCmd(c, "rm", "-f", "test-network-container")
	dockerCmd(c, "network", "rm", "test-event-network-local")

	out, _ := dockerCmd(c, "events", fmt.Sprintf("--since=%d", since), fmt.Sprintf("--until=%d", daemonTime(c).Unix()))
	events := strings.Split(strings.TrimSpace(out), "\n")
	c.Assert(len(events), checker.GreaterThan, 4)

	netEvents := eventActionsByIDAndType(c, events, "test-event-network-local", "network")
	c.Assert(netEvents, checker.HasLen, 4)
	c.Assert(netEvents[0], checker.Equals, "create")
	c.Assert(netEvents[1], checker.Equals, "connect")
	c.Assert(netEvents[2], checker.Equals, "disconnect")
	c.Assert(netEvents[3], checker.Equals, "destroy")
}

func (s *DockerSuite) TestEventsStreaming(c *check.C) {
	testRequires(c, DaemonIsLinux)

	observer, err := newEventObserver(c)
	c.Assert(err, checker.IsNil)
	err = observer.Start()
	c.Assert(err, checker.IsNil)
	defer observer.Stop()

	out, _ := dockerCmd(c, "run", "-d", "busybox:latest", "true")
	containerID := strings.TrimSpace(out)

	testActions := map[string]chan bool{
		"create":  make(chan bool, 1),
		"start":   make(chan bool, 1),
		"die":     make(chan bool, 1),
		"destroy": make(chan bool, 1),
	}

	matcher := matchEventLine(containerID, "container", testActions)
	processor := processEventMatch(testActions)
	go observer.Match(matcher, processor)

	select {
	case <-time.After(5 * time.Second):
		observer.CheckEventError(c, containerID, "create", matcher)
	case <-testActions["create"]:
		// ignore, done
	}

	select {
	case <-time.After(5 * time.Second):
		observer.CheckEventError(c, containerID, "start", matcher)
	case <-testActions["start"]:
		// ignore, done
	}

	select {
	case <-time.After(5 * time.Second):
		observer.CheckEventError(c, containerID, "die", matcher)
	case <-testActions["die"]:
		// ignore, done
	}

	dockerCmd(c, "rm", containerID)

	select {
	case <-time.After(5 * time.Second):
		observer.CheckEventError(c, containerID, "destroy", matcher)
	case <-testActions["destroy"]:
		// ignore, done
	}
}

func (s *DockerSuite) TestEventsImageUntagDelete(c *check.C) {
	testRequires(c, DaemonIsLinux)

	observer, err := newEventObserver(c)
	c.Assert(err, checker.IsNil)
	err = observer.Start()
	c.Assert(err, checker.IsNil)
	defer observer.Stop()

	name := "testimageevents"
	imageID, err := buildImage(name,
		`FROM scratch
		MAINTAINER "docker"`,
		true)
	c.Assert(err, checker.IsNil)
	c.Assert(deleteImages(name), checker.IsNil)

	testActions := map[string]chan bool{
		"untag":  make(chan bool, 1),
		"delete": make(chan bool, 1),
	}

	matcher := matchEventLine(imageID, "image", testActions)
	processor := processEventMatch(testActions)
	go observer.Match(matcher, processor)

	select {
	case <-time.After(10 * time.Second):
		observer.CheckEventError(c, imageID, "untag", matcher)
	case <-testActions["untag"]:
		// ignore, done
	}

	select {
	case <-time.After(10 * time.Second):
		observer.CheckEventError(c, imageID, "delete", matcher)
	case <-testActions["delete"]:
		// ignore, done
	}
}

func (s *DockerSuite) TestEventsFilterVolumeAndNetworkType(c *check.C) {
	testRequires(c, DaemonIsLinux)

	since := daemonTime(c).Unix()

	dockerCmd(c, "network", "create", "test-event-network-type")
	dockerCmd(c, "volume", "create", "--name", "test-event-volume-type")

	out, _ := dockerCmd(c, "events", "--filter", "type=volume", "--filter", "type=network", fmt.Sprintf("--since=%d", since), fmt.Sprintf("--until=%d", daemonTime(c).Unix()))
	events := strings.Split(strings.TrimSpace(out), "\n")
	c.Assert(len(events), checker.GreaterOrEqualThan, 2, check.Commentf(out))

	networkActions := eventActionsByIDAndType(c, events, "test-event-network-type", "network")
	volumeActions := eventActionsByIDAndType(c, events, "test-event-volume-type", "volume")

	c.Assert(volumeActions[0], checker.Equals, "create")
	c.Assert(networkActions[0], checker.Equals, "create")
}

func (s *DockerSuite) TestEventsFilterVolumeID(c *check.C) {
	testRequires(c, DaemonIsLinux)

	since := daemonTime(c).Unix()

	dockerCmd(c, "volume", "create", "--name", "test-event-volume-id")
	out, _ := dockerCmd(c, "events", "--filter", "volume=test-event-volume-id", fmt.Sprintf("--since=%d", since), fmt.Sprintf("--until=%d", daemonTime(c).Unix()))
	events := strings.Split(strings.TrimSpace(out), "\n")
	c.Assert(events, checker.HasLen, 1)

	c.Assert(events[0], checker.Contains, "test-event-volume-id")
	c.Assert(events[0], checker.Contains, "driver=local")
}

func (s *DockerSuite) TestEventsFilterNetworkID(c *check.C) {
	testRequires(c, DaemonIsLinux)

	since := daemonTime(c).Unix()

	dockerCmd(c, "network", "create", "test-event-network-local")
	out, _ := dockerCmd(c, "events", "--filter", "network=test-event-network-local", fmt.Sprintf("--since=%d", since), fmt.Sprintf("--until=%d", daemonTime(c).Unix()))
	events := strings.Split(strings.TrimSpace(out), "\n")
	c.Assert(events, checker.HasLen, 1)

	c.Assert(events[0], checker.Contains, "test-event-network-local")
	c.Assert(events[0], checker.Contains, "type=bridge")
}
