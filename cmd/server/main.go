package main

import (
	"log"

	"github.com/adotkaya/proglog/internal/log"
)

func main() {
	srv := server.NewHTTPServer(":8080")
	log.Fatal(srv.ListenAndServe())
}
