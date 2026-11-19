APP := webfiles

.PHONY: build run clean test

build:
	CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o $(APP) .

run:
	mkdir -p data
	go run . -root ./data

test:
	go test ./...

clean:
	rm -f $(APP)
