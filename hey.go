// Copyright 2014 Google Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Command hey is an HTTP load generator.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"math"
	"net/http"
	gourl "net/url"
	"os"
	"os/signal"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/rakyll/hey/requester"
)

const (
	headerRegexp = `^([\w-]+):\s*(.+)`
	authRegexp   = `^(.+):([^\s].+)`
	heyUA        = "hey/0.0.1"
)

var (
	M = flag.String("M", "", "")

	m           = flag.String("m", "GET", "")
	headers     = flag.String("h", "", "")
	body        = flag.String("d", "", "")
	bodyFile    = flag.String("D", "", "")
	accept      = flag.String("A", "", "")
	contentType = flag.String("T", "text/html", "")
	authHeader  = flag.String("a", "", "")
	hostHeader  = flag.String("host", "", "")

	roomid = flag.String("room", "", "")
	users  = flag.String("users", "", "")
	ip     = flag.String("ip", "", "")

	output = flag.String("o", "", "")

	c = flag.Int("c", 50, "")
	n = flag.Int("n", 200, "")
	q = flag.Float64("q", 0, "")
	t = flag.Int("t", 20, "")
	z = flag.Duration("z", 0, "")

	h2   = flag.Bool("h2", false, "")
	cpus = flag.Int("cpus", runtime.GOMAXPROCS(-1), "")

	disableCompression = flag.Bool("disable-compression", false, "")
	disableKeepAlives  = flag.Bool("disable-keepalive", false, "")
	disableRedirects   = flag.Bool("disable-redirects", false, "")
	proxyAddr          = flag.String("x", "", "")
)

var usage = `Usage: hey [options...] <url>

Options:
  -n  Number of requests to run. Default is 200.
  -c  Number of requests to run concurrently. Total number of requests cannot
      be smaller than the concurrency level. Default is 50.
  -q  Rate limit, in queries per second (QPS). Default is no rate limit.
  -z  Duration of application to send requests. When duration is reached,
      application stops and exits. If duration is specified, n is ignored.
      Examples: -z 10s -z 3m.
  -o  Output type. If none provided, a summary is printed.
      "csv" is the only supported alternative. Dumps the response
      metrics in comma-separated values format.

  -m  HTTP method, one of GET, POST, PUT, DELETE, HEAD, OPTIONS.
  -H  Custom HTTP header. You can specify as many as needed by repeating the flag.
      For example, -H "Accept: text/html" -H "Content-Type: application/xml" .
  -t  Timeout for each request in seconds. Default is 20, use 0 for infinite.
  -A  HTTP Accept header.
  -d  HTTP request body.
  -D  HTTP request body from file. For example, /home/user/file.txt or ./file.txt.
  -T  Content-type, defaults to "text/html".
  -a  Basic authentication, username:password.
  -x  HTTP Proxy address as host:port.
  -h2 Enable HTTP/2.

  -host	HTTP Host header.

  -ip  		server ip.
  -room  	live room id
  -users  	users user ',' sep

  -disable-compression  Disable compression.
  -disable-keepalive    Disable keep-alive, prevents re-use of TCP
                        connections between different HTTP requests.
  -disable-redirects    Disable following of HTTP redirects
  -cpus                 Number of used cpu cores.
                        (default for current machine is %d cores)

`

func main() {
	flag.Usage = func() {
		fmt.Fprint(os.Stderr, fmt.Sprintf(usage, runtime.NumCPU()))
	}

	req, bodyAll := genRequst()

	reqGroups := genLiveReqGroup()

	num := *n
	conc := *c
	q := *q
	dur := *z

	if dur > 0 {
		num = math.MaxInt32
		if conc <= 0 {
			usageAndExit("-c cannot be smaller than 1.")
		}
	} else {
		if num <= 0 || conc <= 0 {
			usageAndExit("-n and -c cannot be smaller than 1.")
		}

		if num < conc {
			usageAndExit("-n cannot be less than -c.")
		}
	}

	var proxyURL *gourl.URL
	if *proxyAddr != "" {
		var err error
		proxyURL, err = gourl.Parse(*proxyAddr)
		if err != nil {
			usageAndExit(err.Error())
		}
	}

	if len(reqGroups) != 0 {
		conc = len(reqGroups)
	}

	w := &requester.Work{
		Request:            req,
		RequestBody:        bodyAll,
		RequstGroups:       reqGroups,
		N:                  num,
		C:                  conc,
		QPS:                q,
		Timeout:            *t,
		DisableCompression: *disableCompression,
		DisableKeepAlives:  *disableKeepAlives,
		DisableRedirects:   *disableRedirects,
		H2:                 *h2,
		ProxyAddr:          proxyURL,
		Output:             *output,
	}
	w.Init()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		<-c
		w.Stop()
	}()
	if dur > 0 {
		go func() {
			time.Sleep(dur)
			w.Stop()
		}()
	}
	w.Run()
}

func genLiveReqGroup() []requester.RequestGroup {
	if !flag.Parsed() {
		flag.Parse()
	}

	if *M != "live" {
		return nil
	}
	if len(*roomid) == 0 || len(*users) == 0 || len(*ip) == 0 {
		usageAndExit("error params")
		return nil
	}

	room := *roomid
	uids := *users
	ipStr := *ip

	userIds := strings.Split(uids, ",")

	list := make([]requester.RequestGroup, 0)

	for _, x := range userIds {
		header := make(http.Header)
		header.Set("Content-Type", *contentType)
		header.Set("X-Putong-User-Id", x)

		url := fmt.Sprintf("http://%s/v2/rooms/%s/members/%s", ipStr, room, x)
		fmt.Println(url)
		req1, err := http.NewRequest(http.MethodPut, url, nil)
		if err != nil {
			usageAndExit(err.Error())
		}
		req1.Header = header

		req2, err := http.NewRequest(http.MethodDelete, url, nil)
		if err != nil {
			usageAndExit(err.Error())
		}
		req2.Header = header

		group := requester.RequestGroup{List: []requester.Request{
			requester.Request{"enter", req1, nil, nil},
			requester.Request{"leave", req2, nil, nil},
		}}

		list = append(list, group)
	}

	return list
}

type GiftRequest struct {
	scenario   string
	originalId string
	roomId     string
	liveId     string
	giftInfo   []GiftInfo
}

type GiftInfo struct {
	giftType string
	num      int
}

func genGiftReqGroup() []requester.RequestGroup {

	if *M != "gift" {
		return nil
	}

	if len(*users) == 0 || flag.NArg() < 1 {
		usageAndExit("error params")
		return nil
	}

	userIds := strings.Split(*users, ",")
	url := flag.Args()[0]
	conc := *c

	list := make([]requester.RequestGroup, 0)
	for i := 0; i < conc; i++ {
		index := i % len(userIds)
		header := make(http.Header)
		header.Set("Content-Type", *contentType)
		header.Set("X-Putong-User-Id", userIds[index])

		req, err := http.NewRequest(http.MethodPost, url, nil)
		if err != nil {
			usageAndExit(err.Error())
		}
		group := requester.RequestGroup{List: []requester.Request{
			requester.Request{"enter", req, nil, func() []byte {
				data := GiftRequest{
					scenario:   "live",
					originalId: fmt.Sprintf("%d_%d", i, time.Now().UnixNano()),
					roomId:     "1",
					liveId:     "3",
					giftInfo:   []GiftInfo{{giftType: "heartbeatLive", num: 1}},
				}
				body, err := json.Marshal(data)
				if err != nil {
					return nil
				}
				return body
			}},
		}}
		list = append(list, group)
	}

	return list
}

func genRequst() (*http.Request, []byte) {
	var hs headerSlice
	flag.Var(&hs, "H", "")
	flag.Parse()

	if *M != "" {
		return nil, nil
	}

	if flag.NArg() < 1 {
		usageAndExit("")
	}

	runtime.GOMAXPROCS(*cpus)

	url := flag.Args()[0]
	method := strings.ToUpper(*m)

	// set content-type
	header := make(http.Header)
	header.Set("Content-Type", *contentType)
	// set any other additional headers
	if *headers != "" {
		usageAndExit("Flag '-h' is deprecated, please use '-H' instead.")
	}
	// set any other additional repeatable headers
	for _, h := range hs {
		match, err := parseInputWithRegexp(h, headerRegexp)
		if err != nil {
			usageAndExit(err.Error())
		}
		header.Set(match[1], match[2])
	}

	if *accept != "" {
		header.Set("Accept", *accept)
	}

	// set basic auth if set
	var username, password string
	if *authHeader != "" {
		match, err := parseInputWithRegexp(*authHeader, authRegexp)
		if err != nil {
			usageAndExit(err.Error())
		}
		username, password = match[1], match[2]
	}

	var bodyAll []byte
	if *body != "" {
		bodyAll = []byte(*body)
	}
	if *bodyFile != "" {
		slurp, err := ioutil.ReadFile(*bodyFile)
		if err != nil {
			errAndExit(err.Error())
		}
		bodyAll = slurp
	}

	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		usageAndExit(err.Error())
	}
	req.ContentLength = int64(len(bodyAll))
	if username != "" || password != "" {
		req.SetBasicAuth(username, password)
	}

	// set host header if set
	if *hostHeader != "" {
		req.Host = *hostHeader
	}

	ua := req.UserAgent()
	if ua == "" {
		ua = heyUA
	} else {
		ua += " " + heyUA
	}
	header.Set("User-Agent", ua)
	req.Header = header

	return req, bodyAll
}

func errAndExit(msg string) {
	fmt.Fprintf(os.Stderr, msg)
	fmt.Fprintf(os.Stderr, "\n")
	os.Exit(1)
}

func usageAndExit(msg string) {
	if msg != "" {
		fmt.Fprintf(os.Stderr, msg)
		fmt.Fprintf(os.Stderr, "\n\n")
	}
	flag.Usage()
	fmt.Fprintf(os.Stderr, "\n")
	os.Exit(1)
}

func parseInputWithRegexp(input, regx string) ([]string, error) {
	re := regexp.MustCompile(regx)
	matches := re.FindStringSubmatch(input)
	if len(matches) < 1 {
		return nil, fmt.Errorf("could not parse the provided input; input = %v", input)
	}
	return matches, nil
}

type headerSlice []string

func (h *headerSlice) String() string {
	return fmt.Sprintf("%s", *h)
}

func (h *headerSlice) Set(value string) error {
	*h = append(*h, value)
	return nil
}
