package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Message struct {
	Type     string
	Sender   string
	SenderID string
	Content  string
}

var onlineUsers = make(map[string]string)
var onlineMu sync.RWMutex // Protects against race conditions from Goroutines
var useANSI = runtime.GOOS != "windows"

func main() {
	senderID := uuid.New().String()
	var username string
	fmt.Print("Choose a username: ")
	fmt.Scanln(&username)
	go runListener(senderID, username)
	onlineMu.Lock()
	onlineUsers = make(map[string]string)
	onlineMu.Unlock()
	pingMessage := buildMessage("ping", username, senderID, "user_count")
	runSender(pingMessage)
	// Allow some time for responses to come in
	time.Sleep(100 * time.Millisecond)
	count := getUserCount()
	names := getOnlineNames()
	fmt.Printf("Welcome! There are %d users online: %v\n", count, names)
	inputLoop(senderID, username)
}

func getUserCount() int {
	onlineMu.RLock()
	defer onlineMu.RUnlock()
	return len(onlineUsers)
}

func getOnlineNames() []string {
	onlineMu.RLock()
	defer onlineMu.RUnlock()
	names := make([]string, 0, len(onlineUsers))
	for _, name := range onlineUsers {
		names = append(names, name)
	}
	return names
}

func inputLoop(senderID, username string) {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("> ")
		text, _ := reader.ReadString('\n')
		text = strings.TrimSpace(text) // Trim newline

		if text == "/online" {
			onlineMu.Lock()
			onlineUsers = make(map[string]string)
			onlineMu.Unlock()
			runSender(buildMessage("ping", username, senderID, "user_count"))
			time.Sleep(100 * time.Millisecond)
			fmt.Printf("Online (%d): %v\n", getUserCount(), getOnlineNames())
			continue
		}

		if text == "" {
			continue
		}

		msg := buildMessage("chat", username, senderID, text)
		runSender(msg)
	}
}

func buildMessage(msgtype, sender, senderID, content string) Message {
	return Message{
		Type:     msgtype,
		Sender:   sender,
		SenderID: senderID,
		Content:  content,
	}
}

func runListener(senderID, username string) {
	addr := net.UDPAddr{IP: net.IPv4(0, 0, 0, 0), Port: 8080}
	conn, err := net.ListenUDP("udp", &addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to listen on UDP %v: %v\n", addr, err)
		return
	}
	defer conn.Close()

	buf := make([]byte, 1024)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			fmt.Println("Error reading from UDP:", err)
			continue
		}

		var msg Message
		err = json.Unmarshal(buf[:n], &msg)
		if err != nil {
			fmt.Println("Error unmarshalling JSON:", err)
			continue
		}

		if msg.SenderID == senderID {
			continue
		}

		// Print incoming messages without leaving a stray prompt char.
		// On terminals that support ANSI escape sequences we clear the
		// current line and overwrite it. On Windows we avoid emitting
		// raw ANSI sequences by using a simple newline-based fallback
		// which prevents weird characters like a left-arrow + 'K'.
		if useANSI {
			clearSeq := "\r\033[K"
			switch msg.Type {
			case "chat":
				fmt.Printf("%s[%s]: %s\n> ", clearSeq, msg.Sender, msg.Content)
			case "ping":
				pong(username, senderID, msg.SenderID)
			case "pong":
				if msg.Content == senderID {
					onlineMu.Lock()
					onlineUsers[msg.SenderID] = msg.Sender
					onlineMu.Unlock()
				}
			default:
				fmt.Printf("%s[Unknown message type from %s]: %s\n> ", clearSeq, msg.Sender, msg.Content)
			}
		} else {
			// Fallback: clear the current line by returning to the start and
			// overwriting with spaces (avoids emitting ANSI sequences).
			// Use a reasonably large width to cover most prompts.
			clearSeq := "\r" + strings.Repeat(" ", 200) + "\r"
			switch msg.Type {
			case "chat":
				fmt.Printf("%s[%s]: %s\n> ", clearSeq, msg.Sender, msg.Content)
			case "ping":
				pong(username, senderID, msg.SenderID)
			case "pong":
				if msg.Content == senderID {
					onlineMu.Lock()
					onlineUsers[msg.SenderID] = msg.Sender
					onlineMu.Unlock()
				}
			default:
				fmt.Printf("%s[Unknown message type from %s]: %s\n> ", clearSeq, msg.Sender, msg.Content)
			}
		}

		//fmt.Printf("Got %s from %s\n", string(buf[:n]), remoteAddr)
	}
}

func runSender(message Message) {
	// Configure address of UDP endpoint
	addr := net.UDPAddr{
		Port: 8080,
		IP:   net.IPv4(255, 255, 255, 255),
	}

	// Create UDP connection - we are dialing, receiver will be listening.
	conn, err := net.DialUDP("udp", nil, &addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to dial UDP %v: %v\n", addr, err)
		return
	}
	// Runs ONLY AFTER main function (outer) exits.
	defer conn.Close()

	// Attempting to set write buffer to 1024 bytes. Can be omitted as OS has a default.
	err = conn.SetWriteBuffer(1024)
	if err != nil {
		fmt.Println("SetWriteBuffer error:", err)
	}

	data, err := json.Marshal(message)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to marshal message: %v\n", err)
		return
	}

	// Send the message via the connection made earlier.
	_, err = conn.Write(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write UDP data: %v\n", err)
		return
	}
}

func pong(username, userID, targetID string) {
	pongMessage := buildMessage("pong", username, userID, targetID)
	runSender(pongMessage)
}
