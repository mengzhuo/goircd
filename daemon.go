/*
goircd -- minimalistic simple Internet Relay Chat (IRC) server
Copyright (C) 2014-2015 Sergey Matveev <stargrave@stargrave.org>

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
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	// Max time deadline for client's unresponsiveness
	PingTimeout = time.Second * 180
	// Max idle client's time before PING are sent
	PingThreshold = time.Second * 90
	// Client's aliveness check period
	AlivenessCheck = time.Second * 10
)

var (
	RENickname = regexp.MustCompile("^[a-zA-Z0-9-]{1,9}$")
)

type Daemon struct {
	Verbose            bool
	version            string
	hostname           *string
	motd               *string
	passwords          *string
	clients            map[*Client]bool
	clientAliveness    map[*Client]*ClientAlivenessState
	rooms              map[string]*Room
	roomSinks          map[*Room]chan ClientEvent
	lastAlivenessCheck time.Time
	logSink            chan<- LogEvent
	stateSink          chan<- StateEvent
}

func NewDaemon(version string, hostname, motd, passwords *string, logSink chan<- LogEvent, stateSink chan<- StateEvent) *Daemon {
	daemon := Daemon{
		version: version,
		hostname: hostname,
		motd: motd,
		passwords: passwords,
	}
	daemon.clients = make(map[*Client]bool)
	daemon.clientAliveness = make(map[*Client]*ClientAlivenessState)
	daemon.rooms = make(map[string]*Room)
	daemon.roomSinks = make(map[*Room]chan ClientEvent)
	daemon.logSink = logSink
	daemon.stateSink = stateSink
	return &daemon
}

func (daemon *Daemon) SendLusers(client *Client) {
	lusers := 0
	for client := range daemon.clients {
		if client.registered {
			lusers++
		}
	}
	client.ReplyNicknamed("251", fmt.Sprintf("There are %d users and 0 invisible on 1 servers", lusers))
}

func (daemon *Daemon) SendMotd(client *Client) {
	if daemon.motd == nil || *daemon.motd == "" {
		client.ReplyNicknamed("422", "MOTD File is missing")
		return
	}

	motd, err := ioutil.ReadFile(*daemon.motd)
	if err != nil {
		log.Printf("Can not read motd file %s: %v", *daemon.motd, err)
		client.ReplyNicknamed("422", "Error reading MOTD File")
		return
	}

	client.ReplyNicknamed("375", "- "+*daemon.hostname+" Message of the day -")
	for _, s := range strings.Split(strings.Trim(string(motd), "\n"), "\n") {
		client.ReplyNicknamed("372", "- "+string(s))
	}
	client.ReplyNicknamed("376", "End of /MOTD command")
}

func (daemon *Daemon) SendWhois(client *Client, nicknames []string) {
	for _, nickname := range nicknames {
		nickname = strings.ToLower(nickname)
		found := false
		for c := range daemon.clients {
			if strings.ToLower(c.nickname) != nickname {
				continue
			}
			found = true
			h := c.conn.RemoteAddr().String()
			h, _, err := net.SplitHostPort(h)
			if err != nil {
				log.Printf("Can't parse RemoteAddr %q: %v", h, err)
				h = "Unknown"
			}
			client.ReplyNicknamed("311", c.nickname, c.username, h, "*", c.realname)
			client.ReplyNicknamed("312", c.nickname, *daemon.hostname, *daemon.hostname)
			if c.away != nil {
				client.ReplyNicknamed("301", c.nickname, *c.away)
			}
			subscriptions := []string{}
			for _, room := range daemon.rooms {
				for subscriber := range room.members {
					if subscriber.nickname == nickname {
						subscriptions = append(subscriptions, room.name)
					}
				}
			}
			sort.Strings(subscriptions)
			client.ReplyNicknamed("319", c.nickname, strings.Join(subscriptions, " "))
			client.ReplyNicknamed("318", c.nickname, "End of /WHOIS list")
		}
		if !found {
			client.ReplyNoNickChan(nickname)
		}
	}
}

func (daemon *Daemon) SendList(client *Client, cols []string) {
	var rooms []string
	if (len(cols) > 1) && (cols[1] != "") {
		rooms = strings.Split(strings.Split(cols[1], " ")[0], ",")
	} else {
		rooms = []string{}
		for room := range daemon.rooms {
			rooms = append(rooms, room)
		}
	}
	sort.Strings(rooms)
	for _, room := range rooms {
		r, found := daemon.rooms[room]
		if found {
			client.ReplyNicknamed("322", room, fmt.Sprintf("%d", len(r.members)), r.topic)
		}
	}
	client.ReplyNicknamed("323", "End of /LIST")
}

// Unregistered client workflow processor. Unregistered client:
// * is not PINGed
// * only QUIT, NICK and USER commands are processed
// * other commands are quietly ignored
// When client finishes NICK/USER workflow, then MOTD and LUSERS are send to him.
func (daemon *Daemon) ClientRegister(client *Client, command string, cols []string) {
	switch command {
	case "PASS":
		if len(cols) == 1 || len(cols[1]) < 1 {
			client.ReplyNotEnoughParameters("PASS")
			return
		}
		client.password = cols[1]
	case "NICK":
		if len(cols) == 1 || len(cols[1]) < 1 {
			client.ReplyParts("431", "No nickname given")
			return
		}
		nickname := cols[1]
		// Compatibility with some clients prepending colons to nickname
		nickname = strings.TrimPrefix(nickname, ":")
		for existingClient := range daemon.clients {
			if existingClient.nickname == nickname {
				client.ReplyParts("433", "*", nickname, "Nickname is already in use")
				return
			}
		}
		if !RENickname.MatchString(nickname) {
			client.ReplyParts("432", "*", cols[1], "Erroneous nickname")
			return
		}
		client.nickname = nickname
	case "USER":
		if len(cols) == 1 {
			client.ReplyNotEnoughParameters("USER")
			return
		}
		args := strings.SplitN(cols[1], " ", 4)
		if len(args) < 4 {
			client.ReplyNotEnoughParameters("USER")
			return
		}
		client.username = args[0]
		client.realname = strings.TrimLeft(args[3], ":")
	}
	if client.nickname != "*" && client.username != "" {
		if daemon.passwords != nil && *daemon.passwords != "" {
			if client.password == "" {
				client.ReplyParts("462", "You may not register")
				client.conn.Close()
				return
			}
			contents, err := ioutil.ReadFile(*daemon.passwords)
			if err != nil {
				log.Fatalf("Can no read passwords file %s: %s", *daemon.passwords, err)
				return
			}
			for _, entry := range strings.Split(string(contents), "\n") {
				if entry == "" {
					continue
				}
				if lp := strings.Split(entry, ":"); lp[0] == client.nickname && lp[1] != client.password {
					client.ReplyParts("462", "You may not register")
					client.conn.Close()
					return
				}
			}
		}

		client.registered = true
		client.ReplyNicknamed("001", "Hi, welcome to IRC")
		client.ReplyNicknamed("002", "Your host is "+*daemon.hostname+", running goircd "+daemon.version)
		client.ReplyNicknamed("003", "This server was created sometime")
		client.ReplyNicknamed("004", *daemon.hostname+" goircd o o")
		daemon.SendLusers(client)
		daemon.SendMotd(client)
		log.Println(client, "logged in")
	}
}

// Register new room in Daemon. Create an object, events sink, save pointers
// to corresponding daemon's places and start room's processor goroutine.
func (daemon *Daemon) RoomRegister(name string) (*Room, chan<- ClientEvent) {
	roomNew := NewRoom(daemon.hostname, name, daemon.logSink, daemon.stateSink)
	roomNew.Verbose = daemon.Verbose
	roomSink := make(chan ClientEvent)
	daemon.rooms[name] = roomNew
	daemon.roomSinks[roomNew] = roomSink
	go roomNew.Processor(roomSink)
	return roomNew, roomSink
}

func (daemon *Daemon) HandlerJoin(client *Client, cmd string) {
	args := strings.Split(cmd, " ")
	rooms := strings.Split(args[0], ",")
	var keys []string
	if len(args) > 1 {
		keys = strings.Split(args[1], ",")
	} else {
		keys = []string{}
	}
	for n, room := range rooms {
		if !RoomNameValid(room) {
			client.ReplyNoChannel(room)
			continue
		}
		var key string
		if (n < len(keys)) && (keys[n] != "") {
			key = keys[n]
		} else {
			key = ""
		}
		denied := false
		joined := false
		for roomExisting, roomSink := range daemon.roomSinks {
			if room == roomExisting.name {
				if (roomExisting.key != "") && (roomExisting.key != key) {
					denied = true
				} else {
					roomSink <- ClientEvent{client, EventNew, ""}
					joined = true
				}
				break
			}
		}
		if denied {
			client.ReplyNicknamed("475", room, "Cannot join channel (+k) - bad key")
		}
		if denied || joined {
			continue
		}
		roomNew, roomSink := daemon.RoomRegister(room)
		log.Println("Room", roomNew, "created")
		if key != "" {
			roomNew.key = key
			roomNew.StateSave()
		}
		roomSink <- ClientEvent{client, EventNew, ""}
	}
}

func (daemon *Daemon) Processor(events <-chan ClientEvent) {
	for event := range events {
		now := time.Now()
		client := event.client

		// Check for clients aliveness
		if daemon.lastAlivenessCheck.Add(AlivenessCheck).Before(now) {
			for c := range daemon.clients {
				aliveness, alive := daemon.clientAliveness[c]
				if !alive {
					continue
				}
				if aliveness.timestamp.Add(PingTimeout).Before(now) {
					log.Println(c, "ping timeout")
					c.conn.Close()
					continue
				}
				if !aliveness.pingSent && aliveness.timestamp.Add(PingThreshold).Before(now) {
					if c.registered {
						c.Msg("PING :" + *daemon.hostname)
						aliveness.pingSent = true
					} else {
						log.Println(c, "ping timeout")
						c.conn.Close()
					}
				}
			}
			daemon.lastAlivenessCheck = now
		}

		switch event.eventType {
		case EventNew:
			daemon.clients[client] = true
			daemon.clientAliveness[client] = &ClientAlivenessState{
				pingSent: false,
				timestamp: now,
			}
		case EventDel:
			delete(daemon.clients, client)
			delete(daemon.clientAliveness, client)
			for _, roomSink := range daemon.roomSinks {
				roomSink <- event
			}
		case EventMsg:
			cols := strings.SplitN(event.text, " ", 2)
			command := strings.ToUpper(cols[0])
			if daemon.Verbose {
				log.Println(client, "command", command)
			}
			if command == "QUIT" {
				log.Println(client, "quit")
				delete(daemon.clients, client)
				delete(daemon.clientAliveness, client)
				client.conn.Close()
				continue
			}
			if !client.registered {
				daemon.ClientRegister(client, command, cols)
				continue
			}
			switch command {
			case "AWAY":
				if len(cols) == 1 {
					client.away = nil
					client.ReplyNicknamed("305", "You are no longer marked as being away")
					continue
				}
				msg := strings.TrimLeft(cols[1], ":")
				client.away = &msg
				client.ReplyNicknamed("306", "You have been marked as being away")
			case "JOIN":
				if len(cols) == 1 || len(cols[1]) < 1 {
					client.ReplyNotEnoughParameters("JOIN")
					continue
				}
				daemon.HandlerJoin(client, cols[1])
			case "LIST":
				daemon.SendList(client, cols)
			case "LUSERS":
				daemon.SendLusers(client)
			case "MODE":
				if len(cols) == 1 || len(cols[1]) < 1 {
					client.ReplyNotEnoughParameters("MODE")
					continue
				}
				cols = strings.SplitN(cols[1], " ", 2)
				if cols[0] == client.username {
					if len(cols) == 1 {
						client.Msg("221 " + client.nickname + " +")
					} else {
						client.ReplyNicknamed("501", "Unknown MODE flag")
					}
					continue
				}
				room := cols[0]
				r, found := daemon.rooms[room]
				if !found {
					client.ReplyNoChannel(room)
					continue
				}
				if len(cols) == 1 {
					daemon.roomSinks[r] <- ClientEvent{client, EventMode, ""}
				} else {
					daemon.roomSinks[r] <- ClientEvent{client, EventMode, cols[1]}
				}
			case "MOTD":
				go daemon.SendMotd(client)
			case "PART":
				if len(cols) == 1 || len(cols[1]) < 1 {
					client.ReplyNotEnoughParameters("PART")
					continue
				}
				for _, room := range strings.Split(cols[1], ",") {
					r, found := daemon.rooms[room]
					if !found {
						client.ReplyNoChannel(room)
						continue
					}
					daemon.roomSinks[r] <- ClientEvent{client, EventDel, ""}
				}
			case "PING":
				if len(cols) == 1 {
					client.ReplyNicknamed("409", "No origin specified")
					continue
				}
				client.Reply(fmt.Sprintf("PONG %s :%s", *daemon.hostname, cols[1]))
			case "PONG":
				continue
			case "NOTICE", "PRIVMSG":
				if len(cols) == 1 {
					client.ReplyNicknamed("411", "No recipient given ("+command+")")
					continue
				}
				cols = strings.SplitN(cols[1], " ", 2)
				if len(cols) == 1 {
					client.ReplyNicknamed("412", "No text to send")
					continue
				}
				msg := ""
				target := strings.ToLower(cols[0])
				for c := range daemon.clients {
					if c.nickname == target {
						msg = fmt.Sprintf(":%s %s %s %s", client, command, c.nickname, cols[1])
						c.Msg(msg)
						if c.away != nil {
							client.ReplyNicknamed("301", c.nickname, *c.away)
						}
						break
					}
				}
				if msg != "" {
					continue
				}
				r, found := daemon.rooms[target]
				if !found {
					client.ReplyNoNickChan(target)
				}
				daemon.roomSinks[r] <- ClientEvent{
					client,
					EventMsg,
					command + " " + strings.TrimLeft(cols[1], ":"),
				}
			case "TOPIC":
				if len(cols) == 1 {
					client.ReplyNotEnoughParameters("TOPIC")
					continue
				}
				cols = strings.SplitN(cols[1], " ", 2)
				r, found := daemon.rooms[cols[0]]
				if !found {
					client.ReplyNoChannel(cols[0])
					continue
				}
				var change string
				if len(cols) > 1 {
					change = cols[1]
				} else {
					change = ""
				}
				daemon.roomSinks[r] <- ClientEvent{client, EventTopic, change}
			case "WHO":
				if len(cols) == 1 || len(cols[1]) < 1 {
					client.ReplyNotEnoughParameters("WHO")
					continue
				}
				room := strings.Split(cols[1], " ")[0]
				r, found := daemon.rooms[room]
				if !found {
					client.ReplyNoChannel(room)
					continue
				}
				daemon.roomSinks[r] <- ClientEvent{client, EventWho, ""}
			case "WHOIS":
				if len(cols) == 1 || len(cols[1]) < 1 {
					client.ReplyNotEnoughParameters("WHOIS")
					continue
				}
				cols := strings.Split(cols[1], " ")
				nicknames := strings.Split(cols[len(cols)-1], ",")
				daemon.SendWhois(client, nicknames)
			case "VERSION":
				var debug string
				if daemon.Verbose {
					debug = "debug"
				} else {
					debug = ""
				}
				client.ReplyNicknamed("351", fmt.Sprintf("%s.%s %s :", daemon.version, debug, *daemon.hostname))
			default:
				client.ReplyNicknamed("421", command, "Unknown command")
			}
		}
		if aliveness, alive := daemon.clientAliveness[client]; alive {
			aliveness.timestamp = now
			aliveness.pingSent = false
		}
	}
}
