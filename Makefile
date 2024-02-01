build:
	go build -o client cmd/client/main.go

clean:
	go clean
	rm client