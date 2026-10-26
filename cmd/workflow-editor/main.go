package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/catalystcommunity/magi/editor"
)

func main() {
	port := flag.Int("port", 8092, "Loopback authoring port; zero selects a free port")
	codecs := flag.String("codecs", ".build/workflows", "Compiled standalone workflow client directory")
	flag.Parse()
	if *port < 0 || *port > 65535 || flag.NArg() != 0 {
		log.Fatal("Invalid editor arguments")
	}
	listener, e := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", *port))
	if e != nil {
		log.Fatal("Editor listener failed")
	}
	defer listener.Close()
	origin := "http://" + listener.Addr().String()
	handler, e := workfloweditor.NewHandler(origin, *codecs)
	if e != nil {
		log.Fatal("Build the clients with npm run build:client before starting the editor")
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		end, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if server.Shutdown(end) != nil {
			server.Close()
		}
		close(done)
	}()
	log.Printf("Local workflow editor: %s (no execution or service credentials)", origin)
	if e = server.Serve(listener); e != nil && e != http.ErrServerClosed {
		log.Fatal("Editor stopped unexpectedly")
	}
	<-done
}
