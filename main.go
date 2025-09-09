package main

import (
	"bytes"
	"crypto/tls"
	"flag"
	"io"
	"log"
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

var warnNoDelay sync.Once

func init() {
	flag.IntVar(&routinesCount, "g", 20, "goroutines count")
	flag.StringVar(&filename, "f", "-", "file containing the request")
	flag.StringVar(&host, "h", "", "host")
	flag.StringVar(&port, "p", "", "port")
	flag.BoolVar(&https, "s", false, "is it an https endpoint")
	flag.IntVar(&preflush, "pr", 20, "sleep microseconds before barrier to help flush/coalescing")
	flag.Parse()
	if host == "" {
		log.Fatalln("host is required ! use the -h flag to define it")
	}
	if port == "" {
		if https {
			port = "443"
		} else {
			port = "80"
		}
	}
}

func check(err error) {
	if err != nil {
		log.Fatalln(err)
	}
}

func openFile(filename string) *os.File {
	if filename == "-" {
		return os.Stdin
	}
	file, err := os.Open(filename)
	check(err)
	return file
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
	if https {
		tlsConn := tls.Client(rawConn, &tls.Config{
			InsecureSkipVerify: true,
			ServerName:         host,
		})
		tlsConn.SetDeadline(time.Now().Add(5 * time.Second))
		if err := tlsConn.Handshake(); err != nil {
			_ = rawConn.Close()
			return nil, err
		}
		tlsConn.SetDeadline(time.Time{})
		return tlsConn, nil
	}
	return rawConn, nil
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
		_, err := conn.Read(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				return
			}
			if err == io.EOF {
				return
			}
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
		if len(line) == 0 {
			continue
		}
		if i := strings.IndexByte(line, ':'); i > 0 {
			name := strings.TrimSpace(strings.ToLower(line[:i]))
			if name == "content-length" {
				v := strings.TrimSpace(line[i+1:])
				if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
					if n > int64(len(request)-(idx+4)) {
						break
					}
					cl = int(n)
				}
			}
		}
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
	check(err)
	defer conn.Close()
	trigger := findHTTPTriggerOffset(request)
	prefix := request[:trigger]
	last := request[trigger : trigger+1]
	if err := writeAll(conn, prefix); err != nil {
		check(err)
	}
	if preflush > 0 {
		time.Sleep(time.Microsecond * time.Duration(preflush))
	}
	/* notify main that the request was almost sent */
	ready <- struct{}{}
	/* sync with other goroutines */
	<-barrier
	/* send the last character */
	if err := writeAll(conn, last); err != nil {
		check(err)
	}
	if !https {
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}
	/* we good, notify main and return */
	ready <- struct{}{}
	drain(conn)
}

func main() {
	file := openFile(filename)
	defer file.Close()
	request, err := io.ReadAll(file)
	check(err)
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
