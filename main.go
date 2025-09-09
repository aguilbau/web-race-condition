package main

import (
	"crypto/tls"
	"flag"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"time"
)

var routinesCount int
var filename string
var host string
var port string
var https bool

var warnNoDelay sync.Once

func init() {
	flag.IntVar(&routinesCount, "g", 20, "goroutines count")
	flag.StringVar(&filename, "f", "-", "file containing the request")
	flag.StringVar(&host, "h", "", "host")
	flag.StringVar(&port, "p", "80", "port")
	flag.BoolVar(&https, "s", false, "is it an https endpoint")
	flag.Parse()
	if host == "" {
		log.Fatalln("host is required ! use the -h flag to define it")
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

func spam(https bool, request []byte, host string, barrier chan struct{}, ready chan struct{}) {
	conn, err := connect(https, host)
	check(err)
	defer conn.Close()
	/* send the request, except for one character */
	check(writeAll(conn, request[:len(request)-1]))
	/* notify main that the request was almost sent */
	ready <- struct{}{}
	/* sync with other goroutines */
	<-barrier
	/* send the last character */
	check(writeAll(conn, request[len(request)-1:]))
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
