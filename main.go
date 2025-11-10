package main

import (
	"fmt"
	"net"
	"os"
)

func main() {
	listen, send := ParseFlags(os.Args[1:])

	if listen && send {
		go runListener()
		runSender()
	} else if listen {
		runListener()
	} else if send {
		runSender()
	} else {
		fmt.Println("Usage: program -l (listen), -s (send), or -ls (both)")
		os.Exit(1)
	}
}

func runListener() {
	addr := net.UDPAddr{IP: net.IPv4(0, 0, 0, 0), Port: 8080}
	conn, err := net.ListenUDP("udp", &addr)
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	buf := make([]byte, 1024)
	for {
		n, remoteAddr, _ := conn.ReadFromUDP(buf)
		fmt.Printf("Got %s from %s\n", string(buf[:n]), remoteAddr)
	}
}

func runSender() {
	// Test string to send via UDP to all devices on LAN.
	message := []byte("Hello, World!")

	// Configure address of UDP endpoint
	addr := net.UDPAddr{
		Port: 8080,
		IP:   net.IPv4(255, 255, 255, 255),
	}

	// Create UDP connection - we are dialing, receiver will be listening.
	conn, err := net.DialUDP("udp", nil, &addr)
	if err != nil {
		panic(err)
	}
	// Runs ONLY AFTER main function (outer) exits.
	defer conn.Close()

	// Attempting to set write buffer to 1024 bytes. Can be omitted as OS has a default.
	err = conn.SetWriteBuffer(1024)
	if err != nil {
		fmt.Println("SetWriteBuffer error:", err)
	}

	// Send the message via the connection made earlier.
	_, err = conn.Write(message)
	if err != nil {
		panic(err)
	}

	fmt.Println("UDP message sent successfully!")
}
