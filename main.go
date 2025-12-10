package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
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
	AuthTag  string
}

var (
	onlineUsers  = make(map[string]string)       // senderID -> username
	usernameToID = make(map[string]string)       // username -> senderID (for /pm)
	userAddrs    = make(map[string]*net.UDPAddr) // senderID -> last known UDP address (IP + port 8080)
	lastChat     = make(map[string]time.Time)    // senderID -> time of last chat message

	onlineMu sync.RWMutex // Protects maps above

	useANSI = runtime.GOOS != "windows"

	// Shared UDP connection for sending broadcast messages.
	sendConn *net.UDPConn

	// Our own identity (shared so goroutines can see it).
	mySenderID string
	myUsername string
)

// Duration after which a user with no chat activity is considered "away".
const awayAfter = 5 * time.Minute

// Shared secret used to derive a time-based auth tag.
// Same in all binaries -> all legitimate clients can compute the same tag.
// Changes every minute because we mix in the current minute bucket.
const sharedSecret = "lanshare-secret-7b4e6c0c"

// computeAuthTagForBucket computes a hex-encoded SHA-256 hash over (secret + bucket).
func computeAuthTagForBucket(bucket int64) string {
	h := sha256.New()
	h.Write([]byte(sharedSecret))

	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(bucket))
	h.Write(b[:])

	return hex.EncodeToString(h.Sum(nil))
}

// generateCurrentAuthTag returns the tag for the current minute bucket.
func generateCurrentAuthTag() string {
	currentBucket := time.Now().Unix() / 60
	return computeAuthTagForBucket(currentBucket)
}

// validAuthTag checks if the tag is valid for the current or nearby minute bucket.
// Accepts previous, current, and next minute to allow for minor clock skew (~±1 minute).
func validAuthTag(tag string) bool {
	nowBucket := time.Now().Unix() / 60
	for offset := int64(-1); offset <= 1; offset++ {
		if tag == computeAuthTagForBucket(nowBucket+offset) {
			return true
		}
	}
	return false
}

func main() {
	// UUID for unique identification of users.
	mySenderID = uuid.New().String()

	// Configure address of UDP broadcast endpoint and open a single shared connection.
	bcastAddr := net.UDPAddr{
		Port: 8080,
		IP:   net.IPv4(255, 255, 255, 255),
	}
	conn, err := net.DialUDP("udp", nil, &bcastAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to dial UDP %v: %v\n", bcastAddr, err)
		return
	}
	// Optional attempt to set write buffer to 1024 bytes. Can be omitted as OS has a default.
	if err := conn.SetWriteBuffer(1024); err != nil {
		fmt.Println("SetWriteBuffer error:", err)
	}
	sendConn = conn
	defer conn.Close()

	// Start listener so we can receive pongs (needed for username uniqueness checks and PMs).
	go runListener(mySenderID)

	// Choose username with uniqueness enforcement.
	chooseUniqueUsername()

	// Initial discovery has already run in chooseUniqueUsername, so onlineUsers is populated.
	count := getUserCount()
	names := getOnlineNames()
	fmt.Printf("Welcome, %s! There are %d users online: %v\n", myUsername, count, names)

	// Start consistent background discovery: periodic pings to refresh onlineUsers.
	go periodicDiscovery()

	// Finally, begin accepting user input to send messages.
	inputLoop(mySenderID)
}

// chooseUniqueUsername prompts the user until a username is found that is not
// currently in use by any online user (according to a discovery run).
func chooseUniqueUsername() {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("Choose a username: ")
		name, _ := reader.ReadString('\n')
		name = strings.TrimSpace(name)
		if name == "" {
			fmt.Println("Username cannot be empty.")
			continue
		}

		// Temporarily set global username so pong responses use it if needed.
		myUsername = name

		// Run a discovery (equivalent to /online) to see who is currently online.
		discoverOnlineUsers()

		// Check if any other user is already using this username.
		taken := false
		onlineMu.RLock()
		for id, uname := range onlineUsers {
			if uname == myUsername && id != mySenderID {
				taken = true
				break
			}
		}
		onlineMu.RUnlock()

		if taken {
			fmt.Printf("Username '%s' is already in use. Please choose another.\n", myUsername)
			continue
		}

		// Name is free.
		break
	}
}

// periodicDiscovery runs in a goroutine and performs discovery at fixed intervals,
// keeping onlineUsers reasonably up to date even without manual /online.
func periodicDiscovery() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		discoverOnlineUsers()
	}
}

// discoverOnlineUsers clears the onlineUsers/usernameToID map, broadcasts a ping,
// and waits briefly for pongs to repopulate the map.
func discoverOnlineUsers() {
	onlineMu.Lock()
	onlineUsers = make(map[string]string)
	usernameToID = make(map[string]string)
	onlineMu.Unlock()

	// Build and send ping message to discover other users.
	pingMessage := buildMessage("ping", myUsername, mySenderID, "user_count")
	runSender(pingMessage)

	// Allow some time for responses to come in.
	time.Sleep(100 * time.Millisecond)
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

// Returns two name slices:
// - active: users who are online and have chatted within awayAfter
// - away: users who are online but have not chatted within awayAfter (or never chatted)
func getOnlineStatusLists() (active []string, away []string) {
	onlineMu.RLock()
	defer onlineMu.RUnlock()

	now := time.Now()
	active = make([]string, 0)
	away = make([]string, 0)

	for id, name := range onlineUsers {
		last, ok := lastChat[id]
		if ok && now.Sub(last) <= awayAfter {
			active = append(active, name)
		} else {
			away = append(away, name)
		}
	}
	return active, away
}

// Loop which runs continuously on main thread, accepting user input to send messages.
func inputLoop(senderID string) {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("> ")
		text, _ := reader.ReadString('\n')
		text = strings.TrimSpace(text) // Trim newline

		// Handle commands starting with "/"
		if strings.HasPrefix(text, "/") {
			// Private message: /pm <username> <message...>
			if strings.HasPrefix(text, "/pm") {
				handlePrivateMessage(text, senderID)
				continue
			}

			// Online list
			if text == "/online" {
				// Run a fresh discovery just like at startup.
				discoverOnlineUsers()
				active, away := getOnlineStatusLists()
				fmt.Printf("Online - active (%d): %v\n", len(active), active)
				fmt.Printf("Online - away   (%d): %v\n", len(away), away)
				continue
			}

			// Help command
			if text == "/help" {
				fmt.Println("Available commands:")
				fmt.Println("  /online              - Show online users, split into active and away.")
				fmt.Println("  /pm <user> <message> - Send a private message to a specific user.")
				fmt.Println("  /help                - Show this help text.")
				continue
			}

			// Any other slash-prefixed input is an invalid command.
			fmt.Println("Invalid command, write /help for valid commands.")
			continue
		}

		// Do not send empty messages.
		if text == "" {
			continue
		}

		// If text is not a command, send as normal broadcast chat.
		msg := buildMessage("chat", myUsername, senderID, text)
		runSender(msg)

		// Update our own lastChat so we show as active as well.
		onlineMu.Lock()
		lastChat[senderID] = time.Now()
		onlineMu.Unlock()
	}
}

// Parse and send a private message from the local user.
func handlePrivateMessage(input string, senderID string) {
	// /pm <username> <message...>
	fields := strings.Fields(input)
	if len(fields) < 3 {
		fmt.Println("Usage: /pm <username> <message>")
		return
	}
	targetName := fields[1]
	messageText := strings.TrimSpace(strings.TrimPrefix(input, "/pm "+targetName))

	if messageText == "" {
		fmt.Println("Private message cannot be empty.")
		return
	}

	onlineMu.RLock()
	targetID, okID := usernameToID[targetName]
	addr, okAddr := userAddrs[targetID]
	onlineMu.RUnlock()

	if !okID {
		fmt.Printf("No online user named '%s'.\n", targetName)
		return
	}
	if !okAddr {
		fmt.Printf("User '%s' is online but has no known address yet. Try again later.\n", targetName)
		return
	}

	// Build private message. Type "pm" is handled separately on receiver side.
	msg := buildMessage("pm", myUsername, senderID, messageText)
	sendPrivate(msg, addr)

	// Optionally echo our own PM locally.
	fmt.Printf("[PM to %s]: %s\n", targetName, messageText)

	// Mark ourselves as active.
	onlineMu.Lock()
	lastChat[senderID] = time.Now()
	onlineMu.Unlock()
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
func runListener(senderID string) {
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
		n, remoteAddr, err := conn.ReadFromUDP(buf)
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

		// Verify auth tag before doing anything else.
		if !validAuthTag(msg.AuthTag) {
			// Invalid or missing auth -> ignore message.
			continue
		}

		// Ignore messages sent by ourselves.
		if msg.SenderID == senderID {
			continue
		}

		// For any valid message, remember where this sender lives (for /pm).
		// We want to send PMs to their listening port (8080), not their ephemeral send port.
		onlineMu.Lock()
		userAddrs[msg.SenderID] = &net.UDPAddr{
			IP:   remoteAddr.IP,
			Port: 8080,
		}
		onlineMu.Unlock()

		// Cleaner output by clearing current line before printing incoming message.
		// Windows needed a separate fix here, as it does not support ANSI escape codes by default.
		clearSeq := "\r"
		if useANSI {
			clearSeq = "\r\033[K"
		}

		// Handling of different message types accordingly.
		switch msg.Type {
		case "chat":
			fmt.Printf("%s[%s]: %s\n> ", clearSeq, msg.Sender, msg.Content)
			// Record that this user has been active in chat just now.
			onlineMu.Lock()
			lastChat[msg.SenderID] = time.Now()
			onlineMu.Unlock()
		case "pm":
			// Private messages are unicast to us, so any "pm" we receive is for us.
			fmt.Printf("%s[PM from %s]: %s\n> ", clearSeq, msg.Sender, msg.Content)
			onlineMu.Lock()
			lastChat[msg.SenderID] = time.Now()
			onlineMu.Unlock()
		case "ping":
			pong(myUsername, senderID, msg.SenderID)
		case "pong":
			if msg.Content == senderID {
				onlineMu.Lock()
				onlineUsers[msg.SenderID] = msg.Sender
				usernameToID[msg.Sender] = msg.SenderID
				onlineMu.Unlock()
			}
		// Fallback for unknown message types. Should never be reached.
		default:
			fmt.Printf("%s[Unknown message type from %s]: %s\n> ", clearSeq, msg.Sender, msg.Content)
		}
	}
}

// Broadcast sender (to everyone on the LAN).
func runSender(message Message) {
	if sendConn == nil {
		fmt.Fprintln(os.Stderr, "sendConn is not initialized")
		return
	}

	// Attach a time-based auth tag before sending.
	message.AuthTag = generateCurrentAuthTag()

	// Marshal the Message struct to JSON for sending via UDP.
	data, err := json.Marshal(message)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to marshal message: %v\n", err)
		return
	}

	// Send the message via the shared broadcast connection.
	_, err = sendConn.Write(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write UDP data: %v\n", err)
		return
	}
}

// Unicast sender for private messages to a specific address.
func sendPrivate(message Message, addr *net.UDPAddr) {
	if addr == nil {
		fmt.Fprintln(os.Stderr, "sendPrivate: nil address")
		return
	}

	// Attach a time-based auth tag before sending.
	message.AuthTag = generateCurrentAuthTag()

	data, err := json.Marshal(message)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to marshal private message: %v\n", err)
		return
	}

	// Create a short-lived UDP connection to the recipient's listening port (8080).
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to dial UDP for private message %v: %v\n", addr, err)
		return
	}
	defer conn.Close()

	_, err = conn.Write(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write UDP private data: %v\n", err)
		return
	}
}

// Helper to send a pong response to a ping.
func pong(username, userID, targetID string) {
	pongMessage := buildMessage("pong", username, userID, targetID)
	runSender(pongMessage)
}
