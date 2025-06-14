package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/base32"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
	"net"
	"html/template"
	"io/ioutil"
//	""

	"github.com/miekg/dns"
)

// A regexp for reasonable close-to-valid DNS names
var dnsish = regexp.MustCompile("^[A-Za-z0-9-_.]+$")

// Only one Unbound should run at once, otherwise listen port will collide
var unboundMutex sync.Mutex

var listenAddr = flag.String("listen", ":1232", "The address on which to listen for incoming Web requests")
var unboundAddr = flag.String("unboundAddress", "127.0.0.1:1053", "The address the unbound.conf instructs Unbound to listen on")
var unboundConfig = flag.String("unboundConfig", "unbound.conf", "The path to the unbound.conf file")
var unboundConfigNoV6 = flag.String("unboundConfigNoV6", "unbound-noV6.conf", "The path to unbound.conf with IPv6 disabled")
var unboundConfigNoecs = flag.String("unboundConfigNoecs", "unbound-noecs.conf", "The path to the unbound.conf file with ecs disabled")
var unboundConfigNoecsNoV6 = flag.String("unboundConfigNoecsNoV6", "unbound-noecs-noV6.conf", "The path to unbound.conf with IPv6 and ecs disabled")
var unboundExec = flag.String("unboundExec", "unbound", "The path to the unbound executable")
var indexFile = flag.String("index", "index.html", "The path to index.html")
var response = flag.String("resp", "response.html", "Template html for the response body")

func main() {
	flag.Parse()
	http.HandleFunc("/", indexHandler)
	http.HandleFunc("/conf", configHandler)
	http.HandleFunc("/q", queryHandler)
	http.HandleFunc("/m/", memoryHandler)
	http.ListenAndServe(*listenAddr, nil)
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(*indexFile)
	if err != nil {
		fmt.Fprintln(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	io.Copy(w, file)
	file.Close()
}

func configHandler(w http.ResponseWriter, r *http.Request) {
	file, err := os.Open(*unboundConfig)
	if err != nil {
		fmt.Fprintln(w, err)
		return
	}
	io.Copy(w, file)
	file.Close()
}

type recorder struct {
	sync.Mutex
	archive map[string][]byte
}

func (r *recorder) store(b []byte) string {
	var id [5]byte
	rand.Read(id[:])
	idStr := base32.StdEncoding.EncodeToString(id[:])

	buf := new(bytes.Buffer)
	w := gzip.NewWriter(buf)
	w.Write(b)
	w.Close()

	r.Lock()
	defer r.Unlock()
	r.archive[idStr] = buf.Bytes()
	return idStr
}

func (r *recorder) get(idStr string) ([]byte, error) {
	r.Lock()
	defer r.Unlock()
	gz := r.archive[idStr]
	if gz == nil {
		return nil, nil
	}
	reader, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, err
	}
	bytes, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}

	return bytes, nil
}

var memory = &recorder{
	archive: make(map[string][]byte),
}

type ResponseText struct {
	QueryType string
	Domain string
	Log string
}

func memoryHandler(w http.ResponseWriter, r *http.Request) {
	usingHTML:=false
	components := strings.Split(r.URL.Path[1:], "/")
	if len(components) < 4 {
		http.NotFound(w, r)
		return
	}

	body, err := memory.get(components[3])
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "Error reading logs: %s\n", err)
		return
	}
	if body == nil {
		http.NotFound(w, r)
		return
	}

	content, err := ioutil.ReadFile(*response)
	if err == nil {
		response:=ResponseText{components[1], components[2], string(body)}
		t,err := template.New("foo").Parse(string(content))
		if err==nil {
			err=t.Execute(w, response)
			if err==nil {
				usingHTML=true
			}
		}
	}

	if !usingHTML {
		w.Write(body)
	}

}

func queryHandler(w http.ResponseWriter, r *http.Request) {
	queryParams := r.URL.Query()
	typ, ok := dns.StringToType[queryParams.Get("type")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	qname := queryParams.Get("qname")
	if !dnsish.MatchString(qname) {
		http.NotFound(w, r)
		return
	}
	ecs := false
	ecs0 := true
	if(queryParams.Get("subnet") == "Y") {
		ecs = true
		ecs0 = false
	} else if(queryParams.Get("subnet") == "0") {
		ecs = true
	}
	noV6 := false
	if(queryParams.Get("noV6") == "Y") {
		noV6 = true
	}

	var buf = new(bytes.Buffer)
	doQuery1(r.Context(), qname, typ, ecs, ecs0, noV6, buf)
	idStr := memory.store(buf.Bytes())
	http.Redirect(w, r, fmt.Sprintf("/m/%s/%s/%s", dns.TypeToString[typ], qname, idStr), http.StatusTemporaryRedirect)
}

func doQuery1(ctx context.Context, q string, typ uint16, ecs bool, ecs0 bool, noV6 bool, w io.Writer) {
	fmt.Fprintf(w, "Query results for %s %s\n", dns.TypeToString[typ], q)
	unboundMutex.Lock()
	defer unboundMutex.Unlock()
	err := doQuery(ctx, q, typ, ecs, ecs0, noV6, w)
	if err != nil {
		fmt.Fprintf(w, "\n\nError running query: %s\n", err)
	}
}

func doQuery(ctx context.Context, q string, typ uint16, ecs bool, ecs0 bool, noV6 bool, w io.Writer) error {
	// Automatically make the query name fully-qualified.
	if !strings.HasSuffix(q, ".") {
		q = q + "."
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	chosenConf := unboundConfig
	if(noV6) {
		if(ecs) {
			chosenConf = unboundConfigNoV6
		} else {
			chosenConf = unboundConfigNoecsNoV6			
		}
	} else {
		if(ecs) {
			chosenConf = unboundConfig
		} else {
			chosenConf = unboundConfigNoecs
		}
	}
	cmd := exec.CommandContext(ctx, *unboundExec, "-d", "-c", *chosenConf)
	defer func() {
		cancel()
		cmd.Wait()
	}()
	// Unbound logs will be sent on this channel once done.
	logs := make(chan []byte)
	pipe, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() {
		// Kill Unbound, then finish reading off the logs.
		cancel()
		w.Write(<-logs)
		cmd.Wait()
	}()
	go func() {
		// Read Unbound's stderr logs as they come in, both to avoid blocking and to
		// ensure we show what the logs said even if the query times out.
		buf := new(bytes.Buffer)
		fmt.Fprintln(buf, "----- Unbound logs -----")
		io.Copy(buf, pipe)
		logs <- buf.Bytes()
	}()

	// Wait for Unbound to start listening
	time.Sleep(time.Second)
	m := new(dns.Msg)
	m.SetQuestion(q, typ)
	m.AuthenticatedData = true
	m.SetEdns0(4096, true)
	if(ecs) {
		if o := m.IsEdns0(); o != nil {
			e := new(dns.EDNS0_SUBNET)
			e.Code = dns.EDNS0SUBNET
			e.Family = 1 // IPv4
			if(ecs0) {
				e.Address = net.ParseIP("0.0.0.0").To4()				
				e.SourceNetmask = 1
				e.SourceScope = 0
			} else {
				e.Address = net.ParseIP("100.101.102.0").To4()
				e.SourceNetmask = 24
				e.SourceScope = 0
			}
			o.Option = append(o.Option, e)
		}
	}

	c := new(dns.Client)
	// The default timeout in the dns package is 2 seconds, which is too short for
	// some domains. Increase to 30 seconds, limited by the context if applicable.
	// Also retry on timeouts.
	c.Timeout = time.Second * 30
	for {
		in, _, err := c.ExchangeContext(ctx, m, *unboundAddr)
		if err != nil {
			return err
		}
		if err == nil {
			fmt.Fprintf(w, "\nResponse:\n%s\n", in)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			continue
		}
	}
}
