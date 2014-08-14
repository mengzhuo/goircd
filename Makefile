LDFLAGS="-X main.version \"$(shell git describe --tags)\""

goircd:
	go install -ldflags $(LDFLAGS) $(BUILD_FLAGS)
