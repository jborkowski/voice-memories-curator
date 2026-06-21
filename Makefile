.PHONY: build run clean

build:
	CGO_ENABLED=1 go build -o vmc .

run: build
	./vmc

clean:
	rm -f vmc
