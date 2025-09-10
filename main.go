package main

import (
	"bytes"
	"crypto/tls"
	"flag"
	"io"
	"log"
	"math/rand"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var routinesCount int
var filename string
var host string
var port string
var https bool
var preflush int
var jitter int

var warnNoDelay sync.Once

func init() {
	flag.IntVar(&routinesCount, "goroutines", 20, "number of goroutines")
	flag.StringVar(&filename, "file", "-", "file containing the request")
	flag.StringVar(&host, "host", "", "host")
	flag.StringVar(&port, "port", "", "port")
	flag.BoolVar(&https, "https", false, "is it an https endpoint")
	flag.IntVar(&preflush, "preflush", 20, "sleep microseconds before barrier to help flush/coalescing")
	flag.IntVar(&jitter, "jitter", 0, "random jitter in microseconds before last byte (±J)")
	flag.Parse()
	if host == "" {
		log.Fatalln("host is required ! use the -host flag to define it")
	}
	if port == "" {
		if https {
			port = "443"
		} else {
			port = "80"
		}
	}
	rand.Seed(time.Now().UnixNano())
}

func readFile(filename string) ([]byte, error) {
	if filename == "-" {
		return io.ReadAll(os.Stdin)
	}
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

func connect(https bool, host string) (net.Conn, error) {
	d := net.Dialer{Timeout: 5 * time.Second}
	rawConn, err := d.Dial("tcp", host)
	if err != nil {
		return nil, err
	}
	if tc, ok := rawConn.(*net.TCPConn); ok {
		if err := tc.SetNoDelay(true); err != nil {
			warnNoDelay.Do(func() {
				log.Println("warning: TCP_NODELAY not supported on this OS")
			})
		}
	}
	if !https {
		return rawConn, nil
	}
	name := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		name = h
	}
	tlsConn := tls.Client(rawConn, &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         name,
		NextProtos:         []string{"http/1.1"},
	})
	tlsConn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := tlsConn.Handshake(); err != nil {
		_ = rawConn.Close()
		return nil, err
	}
	tlsConn.SetDeadline(time.Time{})
	return tlsConn, nil
}

func writeAll(conn net.Conn, data []byte) error {
	total := 0
	for total < len(data) {
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		n, err := conn.Write(data[total:])
		if n > 0 {
			total += n
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func drain(conn net.Conn) {
	buf := make([]byte, 32*1024)
	for {
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Read(buf); err != nil {
			return
		}
	}
}

func findHTTPTriggerOffset(request []byte) int {
	idx := bytes.Index(request, []byte("\r\n\r\n"))
	if idx < 0 {
		return len(request) - 1
	}
	headers := string(request[:idx])

	cl := -1
	for _, line := range strings.Split(headers, "\r\n") {
		if line == "" {
			continue
		}
		i := strings.IndexByte(line, ':')
		if i <= 0 {
			continue
		}
		name := strings.TrimSpace(strings.ToLower(line[:i]))
		if name != "content-length" {
			continue
		}
		v := strings.TrimSpace(line[i+1:])
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			continue
		}
		if n > int64(len(request)-(idx+4)) {
			break
		}
		cl = int(n)
	}
	if cl < 0 {
		return idx + 3
	}

	bodyStart := idx + 4
	bodyEnd := bodyStart + cl
	if bodyEnd <= 0 || bodyEnd > len(request) {
		return len(request) - 1
	}
	return bodyEnd - 1
}

func spam(https bool, request []byte, host string, barrier chan struct{}, ready chan struct{}) {
	conn, err := connect(https, host)
	if err != nil {
		log.Println(err)
		ready <- struct{}{}
		ready <- struct{}{}
		return
	}
	defer conn.Close()

	trigger := findHTTPTriggerOffset(request)
	prefix := request[:trigger]
	last := request[trigger : trigger+1]

	if err := writeAll(conn, prefix); err != nil {
		log.Println(err)
		ready <- struct{}{}
		ready <- struct{}{}
		return
	}
	if preflush > 0 {
		time.Sleep(time.Microsecond * time.Duration(preflush))
	}
	/* notify main that the request was almost sent */
	ready <- struct{}{}
	/* sync with other goroutines */
	<-barrier
	if jitter > 0 {
		d := rand.Intn(2*jitter+1) - jitter
		if d > 0 {
			time.Sleep(time.Microsecond * time.Duration(d))
		}
	}
	/* send the last character */
	if err := writeAll(conn, last); err != nil {
		log.Println(err)
	}
	/* read server response */
	drain(conn)
	/* we good, notify main and return */
	ready <- struct{}{}
}

func main() {
	request, err := readFile(filename)
	if err != nil {
		log.Fatalln(err)
	}
	if len(request) == 0 {
		log.Fatalln("request can not be empty")
	}
	/* channel to sync goroutines */
	barrier := make(chan struct{})
	/* channel to help creating / ending goroutines */
	ready := make(chan struct{})

	/* create "routinesCount" goroutines */
	for i := 0; i < routinesCount; i++ {
		go spam(https, request, host+":"+port, barrier, ready)
	}
	/* wait for them to send the request */
	for i := 0; i < routinesCount; i++ {
		<-ready
	}
	/* at this points, goroutines are hanging, trying to receive from "in" */
	close(barrier)
	/* wait for all goroutines to send last character and exit */
	for i := 0; i < routinesCount; i++ {
		<-ready
	}
	close(ready)
}
