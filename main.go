package main

import (
	"bufio"
	"crypto/tls"
	"flag"
	"io/ioutil"
	"log"
	"net"
	"os"
)

var routinesCount int
var filename string
var host string
var port string
var https bool

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
	if https {
		return tls.Dial("tcp", host, &tls.Config{
			InsecureSkipVerify: true,
		})
	}
	return net.Dial("tcp", host)
}

func spam(https bool, request []byte, host string, in chan struct{}, out chan struct{}) {
	conn, err := connect(https, host)
	check(err)
	defer conn.Close()
	/* send the request, except for one character */
	conn.Write(request[:len(request)-1])
	/* notify main that the request was almost sent */
	out <- struct{}{}
	/* sync with other goroutines */
	<-in
	/* send the last character */
	conn.Write(request[len(request)-1:])
	res, err := ioutil.ReadAll(bufio.NewReader(conn))
	if err == nil {
		os.Stdout.Write(res)
	}
	/* we good, notify main and return */
	out <- struct{}{}
}

func main() {
	file := openFile(filename)
	defer file.Close()
	request, err := ioutil.ReadAll(file)
	check(err)
	if len(request) == 0 {
		log.Fatalln("request can not be empty")
	}
	/* channel to sync goroutines */
	in := make(chan struct{})
	/* channel to help creating / ending goroutines */
	out := make(chan struct{})

	/* create "routinesCount" goroutines */
	for i := 0; i < routinesCount; i++ {
		go spam(https, request, host+":"+port, in, out)
	}
	/* wait for them to send the request */
	for i := 0; i < routinesCount; i++ {
		<-out
	}
	/* at this points, goroutines are hanging, trying to receive from "in" */
	close(in)
	/* wait for all goroutines to send last character and exit */
	for i := 0; i < routinesCount; i++ {
		<-out
	}
	close(out)
}
