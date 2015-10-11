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
	"log"
	"regexp"
	"sort"
	"strings"
)

var (
	RERoom = regexp.MustCompile("^#[^\x00\x07\x0a\x0d ,:/]{1,200}$")

	rooms map[string]*Room = make(map[string]*Room)

	roomSinks map[*Room]chan ClientEvent = make(map[*Room]chan ClientEvent)
)

// Sanitize room's name. It can consist of 1 to 50 ASCII symbols
// with some exclusions. All room names will have "#" prefix.
func RoomNameValid(name string) bool {
	return RERoom.MatchString(name)
}

type Room struct {
	name    *string
	topic   *string
	key     *string
	members map[*Client]struct{}
}

func (room Room) String() string {
	return *room.name
}

func NewRoom(name string) *Room {
	topic := ""
	return &Room{
		name:    &name,
		topic:   &topic,
		members: make(map[*Client]struct{}),
	}
}

func (room *Room) SendTopic(client *Client) {
	if *room.topic == "" {
		client.ReplyNicknamed("331", *room.name, "No topic is set")
	} else {
		client.ReplyNicknamed("332", *room.name, *room.topic)
	}
}

// Send message to all room's subscribers, possibly excluding someone.
func (room *Room) Broadcast(msg string, clientToIgnore ...*Client) {
	for member := range room.members {
		if (len(clientToIgnore) > 0) && member == clientToIgnore[0] {
			continue
		}
		member.Msg(msg)
	}
}

func (room *Room) StateSave() {
	var key string
	if room.key != nil {
		key = *room.key
	}
	stateSink <- StateEvent{*room.name, *room.topic, key}
}

func (room *Room) Processor(events <-chan ClientEvent) {
	var client *Client
	for event := range events {
		client = event.client
		switch event.eventType {
		case EventTerm:
			roomsGroup.Done()
			return
		case EventNew:
			room.members[client] = struct{}{}
			if *verbose {
				log.Println(client, "joined", room.name)
			}
			room.SendTopic(client)
			room.Broadcast(fmt.Sprintf(":%s JOIN %s", client, *room.name))
			logSink <- LogEvent{*room.name, *client.nickname, "joined", true}
			nicknames := make([]string, 0)
			for member := range room.members {
				nicknames = append(nicknames, *member.nickname)
			}
			sort.Strings(nicknames)
			client.ReplyNicknamed("353", "=", *room.name, strings.Join(nicknames, " "))
			client.ReplyNicknamed("366", *room.name, "End of NAMES list")
		case EventDel:
			if _, subscribed := room.members[client]; !subscribed {
				client.ReplyNicknamed("442", *room.name, "You are not on that channel")
				continue
			}
			delete(room.members, client)
			msg := fmt.Sprintf(":%s PART %s :%s", client, *room.name, *client.nickname)
			room.Broadcast(msg)
			logSink <- LogEvent{*room.name, *client.nickname, "left", true}
		case EventTopic:
			if _, subscribed := room.members[client]; !subscribed {
				client.ReplyParts("442", *room.name, "You are not on that channel")
				continue
			}
			if event.text == "" {
				room.SendTopic(client)
				continue
			}
			topic := strings.TrimLeft(event.text, ":")
			room.topic = &topic
			msg := fmt.Sprintf(":%s TOPIC %s :%s", client, *room.name, *room.topic)
			room.Broadcast(msg)
			logSink <- LogEvent{
				*room.name,
				*client.nickname,
				"set topic to " + *room.topic,
				true,
			}
			room.StateSave()
		case EventWho:
			for m := range room.members {
				client.ReplyNicknamed(
					"352",
					*room.name,
					*m.username,
					m.conn.RemoteAddr().String(),
					*hostname,
					*m.nickname,
					"H",
					"0 "+*m.realname,
				)
			}
			client.ReplyNicknamed("315", *room.name, "End of /WHO list")
		case EventMode:
			if event.text == "" {
				mode := "+"
				if room.key != nil {
					mode = mode + "k"
				}
				client.Msg(fmt.Sprintf("324 %s %s %s", *client.nickname, *room.name, mode))
				continue
			}
			if strings.HasPrefix(event.text, "b") {
				client.ReplyNicknamed("368", *room.name, "End of channel ban list")
				continue
			}
			if strings.HasPrefix(event.text, "-k") || strings.HasPrefix(event.text, "+k") {
				if _, subscribed := room.members[client]; !subscribed {
					client.ReplyParts("442", *room.name, "You are not on that channel")
					continue
				}
			} else {
				client.ReplyNicknamed("472", event.text, "Unknown MODE flag")
				continue
			}
			var msg string
			var msgLog string
			if strings.HasPrefix(event.text, "+k") {
				cols := strings.Split(event.text, " ")
				if len(cols) == 1 {
					client.ReplyNotEnoughParameters("MODE")
					continue
				}
				room.key = &cols[1]
				msg = fmt.Sprintf(":%s MODE %s +k %s", client, *room.name, *room.key)
				msgLog = "set channel key to " + *room.key
			} else {
				room.key = nil
				msg = fmt.Sprintf(":%s MODE %s -k", client, *room.name)
				msgLog = "removed channel key"
			}
			room.Broadcast(msg)
			logSink <- LogEvent{*room.name, *client.nickname, msgLog, true}
			room.StateSave()
		case EventMsg:
			sep := strings.Index(event.text, " ")
			room.Broadcast(fmt.Sprintf(
				":%s %s %s :%s",
				client,
				event.text[:sep],
				*room.name,
				event.text[sep+1:]),
				client,
			)
			logSink <- LogEvent{
				*room.name,
				*client.nickname,
				event.text[sep+1:],
				false,
			}
		}
	}
}
