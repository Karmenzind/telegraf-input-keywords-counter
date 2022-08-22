// Package keywords_counter provides ...
package keywords_counter

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/influxdata/tail"
	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/metric"
	"github.com/influxdata/telegraf/plugins/inputs"
	"github.com/influxdata/telegraf/plugins/parsers"
	"github.com/influxdata/telegraf/plugins/parsers/csv"
)

const (
	defaultWatchMethod         = "inotify"
	defaultMaxUndeliveredLines = 1000
)

var (
	offsets      = make(map[string]int64)
	offsetsMutex = new(sync.Mutex)
)

type empty struct{}
type semaphore chan empty

type Logmonitor struct {
	Files               []string `toml:"files"`
	FromBeginning       bool     `toml:"from_beginning"`
	Pipe                bool     `toml:"pipe"`
	WatchMethod         string   `toml:"watch_method"`
	MaxUndeliveredLines int      `toml:"max_undelivered_lines"`
	Keywords            []string `toml:"keywords"`
	KeywordsLower       []string `toml:"-"`

	Log        telegraf.Logger `toml:"-"`
	tailers    map[string]*tail.Tail
	counters   map[string][]int
	offsets    map[string]int64
	parserFunc parsers.ParserFunc
	wg         sync.WaitGroup
	ctx        context.Context
	cancel     context.CancelFunc
	acc        telegraf.TrackingAccumulator
	// acc        telegraf.Accumulator
	sem semaphore
}

func NewLogmonitor() *Logmonitor {
	offsetsMutex.Lock()
	offsetsCopy := make(map[string]int64, len(offsets))
	for k, v := range offsets {
		offsetsCopy[k] = v
	}
	offsetsMutex.Unlock()

	return &Logmonitor{
		FromBeginning:       false,
		MaxUndeliveredLines: 1000,
		offsets:             offsetsCopy,
	}
}

const sampleConfig = `
  ## File names or a pattern to tail.
  ## These accept standard unix glob matching rules, but with the addition of
  ## ** as a "super asterisk". ie:
  ##   "/var/log/**.log"  -> recursively find all .log files in /var/log
  ##   "/var/log/*/*.log" -> find all .log files with a parent dir in /var/log
  ##   "/var/log/apache.log" -> just tail the apache log file
  ##
  ## See https://github.com/gobwas/glob for more examples
  ##
  files = ["/var/mymetrics.out"]

  ## Read file from beginning.
  # from_beginning = false

  ## Whether file is a named pipe
  # pipe = false

  ## Method used to watch for file updates.  Can be either "inotify" or "poll".
  # watch_method = "inotify"

  ## Maximum lines of the file to process that have not yet be written by the
  ## output.  For best throughput set based on the number of metrics on each
  ## line and the size of the output's metric_batch_size.
  # max_undelivered_lines = 1000

  ## Data format to consume.
  ## Each data format has its own unique set of configuration options, read
  ## more about them here:
  ## https://github.com/influxdata/telegraf/blob/master/docs/DATA_FORMATS_INPUT.md
  data_format = "influx"
`

func (t *Logmonitor) SampleConfig() string {
	return sampleConfig
}

func (t *Logmonitor) Description() string {
	return "Stream a log file, like the tail -f command"
}

func (t *Logmonitor) Init() error {
	if t.MaxUndeliveredLines == 0 {
		return errors.New("max_undelivered_lines must be positive")
	}
	t.sem = make(semaphore, t.MaxUndeliveredLines)

	if len(t.Keywords) > 0 {
		t.KeywordsLower = make([]string, 0, len(t.Keywords))
		for i := 0; i < len(t.Keywords); i++ {
			t.KeywordsLower = append(t.KeywordsLower, strings.ToLower(t.Keywords[i]))
		}
	}
	return nil
}

func (t *Logmonitor) Gather(acc telegraf.Accumulator) error {
	err := t.tailNewFiles(true)
	if err == nil {
		now := time.Now()
		metrics := make([]telegraf.Metric, 0, len(t.Keywords)*len(t.counters))

		for filename, counter := range t.counters {
			for i := 0; i < len(t.Keywords); i++ {
				// m, err := metric.New(
				m := metric.New(
					"keywords_counter",
					map[string]string{"file": filename, "keyword": t.Keywords[i]},
					map[string]interface{}{"count": counter[i]},
					now,
				)
				counter[i] = 0
				if err != nil {
					t.Log.Warnf("Failed to create metric: %s", err)
					continue
				}
				metrics = append(metrics, m)
				acc.AddMetric(m)
			}
		}
	}
	return nil
}

func (t *Logmonitor) Start(acc telegraf.Accumulator) error {
	t.ctx, t.cancel = context.WithCancel(context.Background())

	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		for {
			select {
			case <-t.ctx.Done():
				return
				// case <-t.acc.Delivered():
				// 	<-t.sem
			default:
				time.Sleep(50 * time.Millisecond)
			}
		}
	}()

	t.tailers = make(map[string]*tail.Tail)
	// FIXME (qk): <2022-08-11> 后续改成mutex
	t.counters = make(map[string][]int)

	err := t.tailNewFiles(t.FromBeginning)

	// clear offsets
	t.offsets = make(map[string]int64)
	// assumption that once Start is called, all parallel plugins have already been initialized
	offsetsMutex.Lock()
	offsets = make(map[string]int64)
	offsetsMutex.Unlock()

	return err
}

func (t *Logmonitor) tailNewFiles(fromBeginning bool) error {
	var poll bool
	if t.WatchMethod == "poll" {
		poll = true
	}

	// Create a "tailer" for each file
	for _, filepath := range t.Files {
		g, err := Compile(filepath)
		if err != nil {
			t.Log.Errorf("Glob %q failed to compile: %s", filepath, err.Error())
		}
		for _, file := range g.Match() {
			if _, ok := t.tailers[file]; ok {
				// we're already tailing this file
				continue
			}
			// t.Log.Errorf("file %s %s", file, len(t.Keywords))
			if _, ok := t.counters[file]; !ok {
				t.counters[file] = make([]int, len(t.Keywords), len(t.Keywords))
			}

			var seek *tail.SeekInfo
			if !t.Pipe && !fromBeginning {
				if offset, ok := t.offsets[file]; ok {
					t.Log.Debugf("Using offset %d for %q", offset, file)
					seek = &tail.SeekInfo{
						Whence: 0,
						Offset: offset,
					}
				} else {
					seek = &tail.SeekInfo{
						Whence: 2,
						Offset: 0,
					}
				}
			}

			tailer, err := tail.TailFile(file,
				tail.Config{
					ReOpen:    true,
					Follow:    true,
					Location:  seek,
					MustExist: true,
					Poll:      poll,
					Pipe:      t.Pipe,
					Logger:    tail.DiscardingLogger,
				})
			if err != nil {
				t.Log.Debugf("Failed to open file (%s): %v", file, err)
				continue
			}

			t.Log.Debugf("Tail added for %q", file)

			// create a goroutine for each "tailer"
			t.wg.Add(1)
			go func() {
				defer t.wg.Done()
				t.receiver(tailer)

				t.Log.Debugf("Tail removed for %q", tailer.Filename)

				if err := tailer.Err(); err != nil {
					t.Log.Errorf("Tailing %q: %s", tailer.Filename, err.Error())
				}
			}()
			t.tailers[tailer.Filename] = tailer
		}
	}
	for filename, counter := range t.counters {
		for idx, count := range counter {
			keyword := t.Keywords[idx]
			t.Log.Infof("file: %s keyword: %s count: %d", filename, keyword, count)
		}
	}
	return nil
}

func (t *Logmonitor) parseKeywords(counter []int, line string) error {
	var err error
	for idx, keyword := range t.KeywordsLower {
		if len(keyword) == 0 {
			continue
		}
		counter[idx] += strings.Count(strings.ToLower(line), keyword)
	}
	return err
}

// ParseLine parses a line of text.
func parseLine(parser parsers.Parser, line string, firstLine bool) ([]telegraf.Metric, error) {
	switch parser.(type) {
	case *csv.Parser:
		// The csv parser parses headers in Parse and skips them in ParseLine.
		// As a temporary solution call Parse only when getting the first
		// line from the file.
		if firstLine {
			return parser.Parse([]byte(line))
		} else {
			m, err := parser.ParseLine(line)
			if err != nil {
				return nil, err
			}

			if m != nil {
				return []telegraf.Metric{m}, nil
			}
			return []telegraf.Metric{}, nil
		}
	default:
		return parser.Parse([]byte(line))
	}
}

// Receiver is launched as a goroutine to continuously watch a tailed logfile
// for changes, parse any incoming msgs, and add to the accumulator.
// Update:
//  分析关键字，记录次数（后续可能记录行数），给出次数
func (t *Logmonitor) receiver(tailer *tail.Tail) {
	var firstLine = true

	for line := range tailer.Lines {
		if line.Err != nil {
			t.Log.Errorf("Tailing %q: %s", tailer.Filename, line.Err.Error())
			continue
		}
		// Fix up files with Windows line endings.
		text := strings.TrimRight(line.Text, "\r")

		// t.Log.Debugf("Got line: `%s`", text)
		err := t.parseKeywords(t.counters[tailer.Filename], text)
		// metrics, err := parseLine(parser, text, firstLine)
		if err != nil {
			t.Log.Errorf("Malformed log line in %q: [%q]: %s",
				tailer.Filename, line.Text, err.Error())
			continue
		}
		firstLine = false
		_ = firstLine

		// for _, metric := range metrics {
		// 	metric.AddTag("path", tailer.Filename)
		// }

		// Block until plugin is stopping or room is available to add metrics.
		select {
		case <-t.ctx.Done():
			return
			// case t.sem <- empty{}:
			// 	// t.acc.AddTrackingMetricGroup(metrics)
		default:
			// do nothing
			// time.Sleep(50 * time.Millisecond)
		}

	}
}

func (t *Logmonitor) Stop() {
	for _, tailer := range t.tailers {
		if !t.Pipe && !t.FromBeginning {
			// store offset for resume
			offset, err := tailer.Tell()
			if err == nil {
				t.Log.Debugf("Recording offset %d for %q", offset, tailer.Filename)
			} else {
				t.Log.Errorf("Recording offset for %q: %s", tailer.Filename, err.Error())
			}
		}
		err := tailer.Stop()
		if err != nil {
			t.Log.Errorf("Stopping tail on %q: %s", tailer.Filename, err.Error())
		}
	}

	t.cancel()
	t.wg.Wait()

	// persist offsets
	offsetsMutex.Lock()
	for k, v := range t.offsets {
		offsets[k] = v
	}
	offsetsMutex.Unlock()
}

func (t *Logmonitor) SetParserFunc(fn parsers.ParserFunc) {
	t.parserFunc = fn
}

func init() {
	inputs.Add("keywords_counter", func() telegraf.Input {
		return NewLogmonitor()
	})
}
