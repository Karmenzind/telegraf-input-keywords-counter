bin_dir = ./bin
binary = keywords_counter

all: clean build

build:
	mkdir -p $(bin_dir)
	go build -o $(bin_dir)/$(binary) cmd/main.go

clean:
	rm -rf $(bin_dir)

# A much better alternative to 'ls' https://the.exa.website/
overview:
	exa -lhTa --no-user --no-time -I ".git|.vagrant"

restart: 
	sudo systemctl restart telegraf

rundev: build
	./bin/keywords_counter -config ./bin/telegraf_keywords_counter.conf
