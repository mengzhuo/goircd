/*
goircd -- minimalistic simple Internet Relay Chat (IRC) server
Copyright (C) 2014 Sergey Matveev <stargrave@stargrave.org>

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <http://www.gnu.org/licenses/>.
*/
package main

import (
	"crypto/tls"
	"flag"
	"io/ioutil"
	"log"
	"net"
	"path"
	"path/filepath"
	"strings"
)

var (
	hostname = flag.String("hostname", "localhost", "Hostname")
	bind     = flag.String("bind", ":6667", "Address to bind to")
	motd     = flag.String("motd", "", "Path to MOTD file")
	logdir   = flag.String("logdir", "", "Absolute path to directory for logs")
	statedir = flag.String("statedir", "", "Absolute path to directory for states")

	tlsKey  = flag.String("tls_key", "", "TLS keyfile")
	tlsCert = flag.String("tls_cert", "", "TLS certificate")

	verbose = flag.Bool("v", false, "Enable verbose logging.")
)

func Run() {
	var client *Client
	events := make(chan ClientEvent)
	log.SetFlags(log.Ldate | log.Lmicroseconds | log.Lshortfile)

	logSink := make(chan LogEvent)
	if *logdir == "" {
		// Dummy logger
		go func() {
			for _ = range logSink {
			}
		}()
	} else {
		if !path.IsAbs(*logdir) {
			log.Fatalln("Need absolute path for logdir")
			return
		}
		go Logger(*logdir, logSink)
		log.Println(*logdir, "logger initialized")
	}

	stateSink := make(chan StateEvent)
	daemon := NewDaemon(*hostname, *motd, logSink, stateSink)
	daemon.Verbose = *verbose
	if *statedir == "" {
		// Dummy statekeeper
		go func() {
			for _ = range stateSink {
			}
		}()
	} else {
		if !path.IsAbs(*statedir) {
			log.Fatalln("Need absolute path for statedir")
		}
		states, err := filepath.Glob(path.Join(*statedir, "#*"))
		if err != nil {
			log.Fatalln("Can not read statedir", err)
		}
		for _, state := range states {
			buf, err := ioutil.ReadFile(state)
			if err != nil {
				log.Fatalf("Can not read state %s: %v", state, err)
			}
			room, _ := daemon.RoomRegister(path.Base(state))
			contents := strings.Split(string(buf), "\n")
			if len(contents) < 2 {
				log.Printf("State corrupted for %s: %q", room.name, contents)
			} else {
				room.topic = contents[0]
				room.key = contents[1]
				log.Println("Loaded state for room", room.name)
			}
		}
		go StateKeeper(*statedir, stateSink)
		log.Println(*statedir, "statekeeper initialized")
	}

	var listener net.Listener
	if *tlsKey != "" {
		cert, err := tls.LoadX509KeyPair(*tlsCert, *tlsKey)
		if err != nil {
			log.Fatalf("Could not load TLS keys from %s and %s: %s", *tlsCert, *tlsKey, err)
		}
		config := tls.Config{Certificates: []tls.Certificate{cert}}
		listener, err = tls.Listen("tcp", *bind, &config)
		if err != nil {
			log.Fatalf("Can not listen on %s: %v", *bind, err)
		}
	} else {
		var err error
		listener, err = net.Listen("tcp", *bind)
		if err != nil {
			log.Fatalf("Can not listen on %s: %v", *bind, err)
		}
	}
	log.Println("Listening on", *bind)

	go daemon.Processor(events)
	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Println("Error during accepting connection", err)
			continue
		}
		client = NewClient(*hostname, conn)
		go client.Processor(events)
	}
}

func main() {
	flag.Parse()
	Run()
}
