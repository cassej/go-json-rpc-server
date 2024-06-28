package main

import (
    "bufio"
    "io/ioutil"
    "net"
    "net/http"
    "strings"
    "time"
    "context"
)

type Request struct {
    Data   string
    Token  string
    Conn   net.Conn
    Writer http.ResponseWriter
    Done   chan bool
}

func (r *Request) Response(message string) {
    if r.Conn != nil {
        _, err := r.Conn.Write([]byte(message + "\n"))
        if err != nil {
            ErrorLog.Printf("Failed to write to TCP connection: %v", err)
        }
    } else if r.Writer != nil {
        _, err := r.Writer.Write([]byte(message))
        if err != nil {
            ErrorLog.Printf("Failed to write to HTTP connection: %v", err)
        }
    }
    r.Done <- true
}

var requestQueue chan Request

func TCPServer(ctx context.Context) {
    address := config.Server.Host + ":" + config.Server.Port
    listener, err := net.Listen("tcp", address)

    if err != nil {
        ErrorLog.Fatalf("Failed to start TCP server on %s: %v", address, err)
    }

    defer listener.Close()

    NoticeLog.Printf("TCP server listening on %s", address)

    go func() {
        <-ctx.Done()
        listener.Close()
    }()

    for {
        conn, err := listener.Accept()
        if err != nil {
            if ctx.Err() != nil {
                return  // Сервер завершает работу
            }
            ErrorLog.Printf("Failed to accept connection: %v", err)
            continue
        }

        go handleTCPConnection(conn)
    }
}

func HTTPServer() *http.Server {
    server := &http.Server{
        Addr:         config.Server.Host + ":" + config.Server.Port,
        ReadTimeout:  10 * time.Second,
        WriteTimeout: 10 * time.Second,
        Handler:      http.HandlerFunc(handleHTTP),
    }

    return server
}

func handleTCPConnection(conn net.Conn) {
    defer conn.Close()
    reader := bufio.NewReader(conn)

    for {
        err := conn.SetDeadline(time.Now().Add(5 * time.Minute))
        if err != nil {
            ErrorLog.Printf("Failed to set deadline: %v", err)
            return
        }

        message, err := reader.ReadString('\n')
        if err != nil {
            if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
                ErrorLog.Printf("Connection timeout")
            } else {
                ErrorLog.Printf("Failed to read from connection: %v", err)
            }
            return
        }

        if message == "" {
            continue
        }

        requestQueue <- Request{Data: message, Conn: conn}
    }
}

func handleHTTP(w http.ResponseWriter, r *http.Request) {
    if r.Method != "POST" {
        http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
        return
    }

    authHeader := r.Header.Get("Authorization")
    token := strings.TrimPrefix(authHeader, "Bearer ")

    body, err := ioutil.ReadAll(r.Body)
    if err != nil {
        http.Error(w, "Failed to read request body", http.StatusInternalServerError)
        return
    }
    if len(body) < 1 {
        http.Error(w, "Bad Request: Body is empty", http.StatusBadRequest)
        return
    }

    w.Header().Set("Content-Type", "application/json")
    done := make(chan bool, 1)
    wg.Add(1)
    requestQueue <- Request{Token: token, Data: string(body), Writer: w, Done: done}
    <-done
    wg.Done()
}