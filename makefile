LDFLAGS=-X main.version \"$(shell git describe --tags)\"

goircd:
	go build -ldflags "$(LDFLAGS)" $(BUILD_FLAGS)
