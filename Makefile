BINARY=brutesubs
SRC=main.go

build:
	go build -ldflags="-s -w" -o $(BINARY) .

build-race:
	go build -race -o $(BINARY)-race .

install:
	go install -ldflags="-s -w" .

test:
	go test -v ./...

small-test:
	printf '%s\n' 'www' 'api' 'admin' 'WWW' 'api' '*invalid!@#' 'mail.test' > /tmp/wl.txt
	printf '%s\n' 'example.com' 'test.com' > /tmp/roots.txt
	rm -rf /tmp/to-brute /tmp/resolved
	./$(BINARY) -roots /tmp/roots.txt -wordlist /tmp/wl.txt -to-brute /tmp/to-brute -resolved /tmp/resolved -generate-only -no-dedup=false
	cat /tmp/to-brute/example.com.txt
	@echo "--- test passed ---"

clean:
	rm -f $(BINARY) $(BINARY)-race
	rm -rf to-brute resolved .cache

.PHONY: build install test clean small-test
