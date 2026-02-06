package main

import (
	"log"
	"net"
	"os"

	ilog "github.com/adotkaya/proglog/internal/log"
	"github.com/adotkaya/proglog/internal/server"
)

func main() {
	l, err := net.Listen("tcp", ":8400")
	if err != nil {
		log.Fatal(err)
	}

	dir, err := os.MkdirTemp("", "proglog")
	if err != nil {
		log.Fatal(err)
	}

	clog, err := ilog.NewLog(dir, ilog.Config{})
	if err != nil {
		log.Fatal(err)
	}

	cfg := &server.Config{
		CommitLog: clog,
	}

	srv, err := server.NewGRPCServer(cfg)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("Server listening on :8400")
	log.Fatal(srv.Serve(l))
}
