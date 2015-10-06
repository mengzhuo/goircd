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
	"bytes"
	"log"
	"net"
	"strings"
	"time"
)

const (
	BufSize = 1500
)

var (
	CRLF []byte = []byte{'\x0d', '\x0a'}
)

type Client struct {
	hostname   *string
	conn       net.Conn
	registered bool
	nickname   string
	username   string
	realname   string
	password   string
	away       *string
}

type ClientAlivenessState struct {
	pingSent  bool
	timestamp time.Time
}

func (client Client) String() string {
	return client.nickname + "!" + client.username + "@" + client.conn.RemoteAddr().String()
}

func NewClient(hostname *string, conn net.Conn) *Client {
	return &Client{hostname: hostname, conn: conn, nickname: "*", password: ""}
}

// Client processor blockingly reads everything remote client sends,
// splits messages by CRLF and send them to Daemon gorouting for processing
// it futher. Also it can signalize that client is unavailable (disconnected).
func (client *Client) Processor(sink chan<- ClientEvent) {
	sink <- ClientEvent{client, EventNew, ""}
	log.Println(client, "New client")
	buf := make([]byte, BufSize*2)
	var n int
	var prev int
	var i int
	var err error
	for {
		if prev == BufSize {
			log.Println(client, "buffer size exceeded, kicking him")
			sink <- ClientEvent{client, EventDel, ""}
			client.conn.Close()
			break
		}
		n, err = client.conn.Read(buf[prev:])
		if err != nil {
			sink <- ClientEvent{client, EventDel, ""}
			break
		}
		prev += n
	CheckMore:
		i = bytes.Index(buf[:prev], CRLF)
		if i == -1 {
			continue
		}
		sink <- ClientEvent{client, EventMsg, string(buf[:i])}
		copy(buf, buf[i+2:prev])
		prev -= (i + 2)
		goto CheckMore
	}
}

// Send message as is with CRLF appended.
func (client *Client) Msg(text string) {
	client.conn.Write(append([]byte(text), CRLF...))
}

// Send message from server. It has ": servername" prefix.
func (client *Client) Reply(text string) {
	client.Msg(":" + *client.hostname + " " + text)
}

// Send server message, concatenating all provided text parts and
// prefix the last one with ":".
func (client *Client) ReplyParts(code string, text ...string) {
	parts := []string{code}
	for _, t := range text {
		parts = append(parts, t)
	}
	parts[len(parts)-1] = ":" + parts[len(parts)-1]
	client.Reply(strings.Join(parts, " "))
}

// Send nicknamed server message. After servername it always has target
// client's nickname. The last part is prefixed with ":".
func (client *Client) ReplyNicknamed(code string, text ...string) {
	client.ReplyParts(code, append([]string{client.nickname}, text...)...)
}

// Reply "461 not enough parameters" error for given command.
func (client *Client) ReplyNotEnoughParameters(command string) {
	client.ReplyNicknamed("461", command, "Not enough parameters")
}

// Reply "403 no such channel" error for specified channel.
func (client *Client) ReplyNoChannel(channel string) {
	client.ReplyNicknamed("403", channel, "No such channel")
}

func (client *Client) ReplyNoNickChan(channel string) {
	client.ReplyNicknamed("401", channel, "No such nick/channel")
}
