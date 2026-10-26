package main

import (
	"context"
	"errors"
	"github.com/catalystcommunity/magi/config"
	"github.com/catalystcommunity/magi/server"
	"log"
	"net"
	"net/http"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	listen := config.Environment("MAGI_LISTEN")
	if listen == "" {
		listen = "127.0.0.1:8080"
	}
	listener, e := net.Listen("tcp", listen)
	if e != nil {
		log.Fatal("Magi listener failed")
	}
	defer listener.Close()
	origin := config.Environment("MAGI_ORIGIN")
	if origin == "" {
		origin = "http://" + listener.Addr().String()
	}
	handler, e := server.New(origin)
	if e != nil {
		log.Fatal("Invalid Magi origin")
	}
	srv := &http.Server{Addr: listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		handler.Drain()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if srv.Shutdown(c) != nil {
			srv.Close()
		}
		close(done)
	}()
	log.Printf("Magi discovery is ready at %s; task execution is disabled", origin)
	if e := srv.Serve(listener); e != nil && !errors.Is(e, http.ErrServerClosed) {
		log.Fatal("Magi listener failed")
	}
	<-done
}
