v1.0

Simple, command line based application for writing messages between multiple devices on a LAN. Written in Go, and utilises only built in packages aside from UUID.

Features and implementation details:
-Instant messaging over UDP between a (within reason) unlimited number of devices on your LAN.
-Command for checking number of connected users. Utilises a ping and response to get number of connected users and their names.
-Goroutines (concurrency) for handling both sending and receiving simultaneously.
-Scalability. Efficient implementations with structs and good programming patterns allow for more features to be added easily.

Plans for the future:
1. GUI
2. More commands
3. TCP version. For such a simple project as this it obviously isn't necessary, but a fun proof of concept.
4. Connecting to devices outside your LAN. Difficult, so more of a future project.
