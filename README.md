# telegraf-input-keywords-counter

It's a Telegraf input plugin to count keywords from continuous growing files (e.g. logs or other streaming files).

## Installation

### Download releases

TODO

### Build from source

Requirements:

- **Golang**: tested on version `go1.18.4 linux/amd64`

Steps:
- Run `make build` (or `go build -o bin/xxx`)
- The binary should be available inside `bin/`

## How to use

Configuration for telegraf:

```toml
# ... other config ...

[[inputs.execd]]
  command = ["/path/to/keywords_counter"]
  signal = "none"

# sample output: write metrics to stdout
[[outputs.file]]
  files = ["stdout"]

# ... other config ...
```

Configuration for keywords counter input:

```toml
[[inputs.keywords_counter]]
files = ["/tmp/top.log", "somepath/*/*/**.log"]
interval = "10s"
from_beginning=false
keywords = ["python", "0.1", "root"]
```

## Output

```
>>> telegraf --config path_to_telegraf_conf --input-filter=execd

2022-08-16T06:48:08Z I! Starting Telegraf
keywords_counter,file=/tmp/top.log,host=hostname,keyword=root count=11i 1660632498940008039
keywords_counter,file=/tmp/top.log,host=hostname,keyword=0.1 count=8i 1660632498940008039
keywords_counter,file=/tmp/top.log,host=hostname,keyword=python count=16i 1660632498940008039
keywords_counter,file=/tmp/top.log,host=hostname,keyword=root count=12i 1660632508939478880
keywords_counter,file=/tmp/top.log,host=hostname,keyword=0.1 count=6i 1660632508939478880
keywords_counter,file=/tmp/top.log,host=hostname,keyword=python count=12i 1660632508939478880
keywords_counter,file=/tmp/top.log,host=hostname,keyword=root count=9i 1660632518939906148
keywords_counter,file=/tmp/top.log,host=hostname,keyword=0.1 count=7i 1660632518939906148
keywords_counter,file=/tmp/top.log,host=hostname,keyword=python count=12i 1660632518939906148
keywords_counter,file=/tmp/top.log,host=hostname,keyword=root count=13i 1660632528939126409
```


## References

- https://github.com/filmineio/telegraf-input-lotus
- https://github.com/ssoroka/rand
