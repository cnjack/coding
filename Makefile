BIN := coding
PKG := ./cmd/coding/

.PHONY: build run doctor version install clean

build:
	go build -o $(BIN) $(PKG)

install:
	go install $(PKG)

run:
	go run $(PKG)

doctor:
	go run $(PKG) --doctor

version:
	go run $(PKG) --version

clean:
	rm -f $(BIN)
