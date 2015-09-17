package logger

import (
	"io"
	"sync"
	"time"

	"github.com/Sirupsen/logrus"
)

// Copier can copy logs from specified sources to Logger and attach
// ContainerID and Timestamp.
// Writes are concurrent, so you need implement some sync in your logger
type Copier struct {
	// cid is the container id for which we are copying logs
	cid string
	// srcs is map of name -> reader pairs, for example "stdout", "stderr"
	srcs     map[string]io.Reader
	dst      Logger
	copyJobs sync.WaitGroup
}

// NewCopier creates a new Copier
func NewCopier(cid string, srcs map[string]io.Reader, dst Logger) *Copier {
	return &Copier{
		cid:  cid,
		srcs: srcs,
		dst:  dst,
	}
}

// Run starts logs copying
func (c *Copier) Run() {
	for src, w := range c.srcs {
		c.copyJobs.Add(1)
		go c.copySrc(src, w)
	}
}

func (c *Copier) copySrc(name string, src io.Reader) {
	defer c.copyJobs.Done()
	buf := make([]byte, 4096)
	for {
		bytesRead, err := src.Read(buf)
		if err == nil || bytesRead > 0 {
			if logErr := c.dst.Log(&Message{ContainerID: c.cid, Line: buf[:bytesRead], Source: name, Timestamp: time.Now().UTC()}); logErr != nil {
				logrus.Errorf("Failed to log msg %q for logger %s: %s", buf[:bytesRead], c.dst.Name(), logErr)
			}
		}

		if err != nil {
			if err != io.EOF {
				logrus.Errorf("Error scanning log stream: %s", err)
			}
			return
		}

	}
}

// Wait waits until all copying is done
func (c *Copier) Wait() {
	c.copyJobs.Wait()
}
