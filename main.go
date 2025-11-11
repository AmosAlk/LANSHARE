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

// Struct allows for pings and actual messages to be sent using the same format.
// They are marshalled and unmarshalled as JSON.
type Message struct {
	Type     string
	Sender   string
	SenderID string
	Content  string
}

var onlineUsers = make(map[string]string) // Maps senderID to username
var onlineMu sync.RWMutex                 // Protects against race conditions from Goroutines
var useANSI = runtime.GOOS != "windows"

func main() {
	// UUID for unique identification of users.
	senderID := uuid.New().String()
	var username string
	fmt.Print("Choose a username: ")
	fmt.Scanln(&username)

	// Separate goroutine to listen, so that sending and receiving can happen simultaneously.
	go runListener(senderID, username)
	// Mutex lock used to prevent race conditions when accessing onlineUsers map.
	onlineMu.Lock()
	onlineUsers = make(map[string]string)
	onlineMu.Unlock()
	// Build and send ping message to discover other users.
	pingMessage := buildMessage("ping", username, senderID, "user_count")
	runSender(pingMessage)
	// Allow some time for responses to come in.
	time.Sleep(100 * time.Millisecond)
	count := getUserCount()
	names := getOnlineNames()
	fmt.Printf("Welcome! There are %d users online: %v\n", count, names)
	// Finally, begin accepting user input to send messages.
	inputLoop(senderID, username)
}

// Unlock deferred until after return.
func getUserCount() int {
	onlineMu.RLock()
	defer onlineMu.RUnlock()
	return len(onlineUsers)
}

// Unlock deferred until after return.
func getOnlineNames() []string {
	onlineMu.RLock()
	defer onlineMu.RUnlock()
	// Collect usernames from the map to print.
	names := make([]string, 0, len(onlineUsers))
	for _, name := range onlineUsers {
		names = append(names, name)
	}
	return names
}

// Loop which runs continuously on main thread, accepting user input to send messages.
func inputLoop(senderID, username string) {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("> ")
		text, _ := reader.ReadString('\n')
		text = strings.TrimSpace(text) // Trim newline

		// Command to check for users online.
		if text == "/online" {
			// Race condition protection.
			onlineMu.Lock()
			onlineUsers = make(map[string]string)
			onlineMu.Unlock()
			// Build and send pings as before, to discover other devices.
			runSender(buildMessage("ping", username, senderID, "user_count"))
			time.Sleep(100 * time.Millisecond)
			fmt.Printf("Online (%d): %v\n", getUserCount(), getOnlineNames())
			continue
		}
		// Do not send empty messages.
		if text == "" {
			continue
		}
		// If text is not a command, send as normal.
		msg := buildMessage("chat", username, senderID, text)
		runSender(msg)
	}
}

// Helper to build a Message struct, much shorter than repeating this everywhere.
func buildMessage(msgtype, sender, senderID, content string) Message {
	return Message{
		Type:     msgtype,
		Sender:   sender,
		SenderID: senderID,
		Content:  content,
	}
}

// Goroutine which listens for incoming UDP messages.
func runListener(senderID, username string) {
	// 0, 0, 0, 0 means listen on all interfaces, with port 8080.
	addr := net.UDPAddr{IP: net.IPv4(0, 0, 0, 0), Port: 8080}
	conn, err := net.ListenUDP("udp", &addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to listen on UDP %v: %v\n", addr, err)
		return
	}
	defer conn.Close()

	// Configuring size is optional, OS has a default buffer size.
	buf := make([]byte, 1024)
	for {
		// Origin address is unused here. Could be logged if desired.
		// SenderID from the Message struct is used for identification purposes instead.
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			fmt.Println("Error reading from UDP:", err)
			continue
		}

		// Unmarshal the JSON message to a Message struct, so it can be processed.
		var msg Message
		err = json.Unmarshal(buf[:n], &msg)
		if err != nil {
			fmt.Println("Error unmarshalling JSON:", err)
			continue
		}

		// Ignore messages sent by ourselves.
		if msg.SenderID == senderID {
			continue
		}

		// Cleaner output by clearing current line before printing incoming message.
		// Windows needed a separate fix here, as it does not support ANSI escape codes by default.
		clearSeq := "\r"
		if useANSI {
			clearSeq = "\r\033[K"
		}

		// Handling of different message types accordingly. Here is where we see the benefit of using
		// a structured common message format, rather than a separate ping struct for example.
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
		// Fallback for unknown message types. Should never be reached.
		default:
			fmt.Printf("%s[Unknown message type from %s]: %s\n> ", clearSeq, msg.Sender, msg.Content)
		}
	}
}

func runSender(message Message) {
	// Configure address of UDP endpoint.
	addr := net.UDPAddr{
		Port: 8080,
		IP:   net.IPv4(255, 255, 255, 255),
	}

	// Create UDP connection. DialUDP used for sending, ListenUDP for receiving.
	conn, err := net.DialUDP("udp", nil, &addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to dial UDP %v: %v\n", addr, err)
		return
	}
	defer conn.Close()

	// Optional attempt to set write buffer to 1024 bytes. Can be omitted as OS has a default.
	err = conn.SetWriteBuffer(1024)
	if err != nil {
		fmt.Println("SetWriteBuffer error:", err)
	}

	// Marshal the Message struct to JSON for sending via UDP.
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

// Helper to send a pong response to a ping.
func pong(username, userID, targetID string) {
	pongMessage := buildMessage("pong", username, userID, targetID)
	runSender(pongMessage)
}
