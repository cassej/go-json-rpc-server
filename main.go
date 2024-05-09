package main

import (
    "os"
	"flag"
	"log"
	"sync"
	"encoding/json"
	"github.com/vmihailenco/msgpack/v5"
	"os/signal"
	"syscall"
)

var ErrorLog = log.New(os.Stderr, "ERROR: ", log.Ldate|log.Ltime)
var NoticeLog = log.New(os.Stderr, "NOTICE: ", log.Ldate|log.Ltime)
var configPath = flag.String("config", "config.json", "path to config file")
var config *Config
var wg sync.WaitGroup

// Serializer - интерфейс для сериализации и десериализации данных.
type Serializer interface {
    Marshal(v interface{}) ([]byte, error)
    Unmarshal(data []byte, v interface{}) error
}

// JSONSerializer - реализация сериализатора для JSON.
type JSONSerializer struct{}

func (js JSONSerializer) Marshal(v interface{}) ([]byte, error) {
    return json.Marshal(v)
}

func (js JSONSerializer) Unmarshal(data []byte, v interface{}) error {
    return json.Unmarshal(data, v)
}

// MsgPackSerializer - реализация сериализатора для MessagePack.
type MsgPackSerializer struct{}

func (mps MsgPackSerializer) Marshal(v interface{}) ([]byte, error) {
    return msgpack.Marshal(v)
}

func (mps MsgPackSerializer) Unmarshal(data []byte, v interface{}) error {
    return msgpack.Unmarshal(data, v)
}

func main() {
	flag.Parse()
    config = LoadConfig(*configPath)

    if config == nil {
        ErrorLog.Fatal("Failed to load configuration")
    }

    rpc.init()

    // Обработка сигнала SIGHUP
    sighupChan := make(chan os.Signal, 1)
    signal.Notify(sighupChan, syscall.SIGHUP)

    go func() {
        for {
            select {
            case <-sighupChan:
                // Перезагрузите методы или конфигурацию
                NoticeLog.Println("Received SIGHUP, reinitializing methods.")
                rpc.init() // Предполагается, что rpc.init() переинициализирует методы.
            }
        }
    }()

    requestQueue = make(chan Request, config.Server.MaxRequests)

	if config.Server.Protocol == "tcp" {
	    go TCPServer()
	} else if config.Server.Protocol == "http" {
	    go HTTPServer()
	}

	for i := 0; i < config.Server.MaxWorkers; i++ {
        wg.Add(1)
        go requestHandler(i)
    }

    wg.Wait()
}

func requestHandler(i int) {
    defer func() {
        if r := recover(); r != nil {
            NoticeLog.Printf("Recovered in requestHandler %d: %v", i, r)
        }
    }()

    for req := range requestQueue {
        var response []byte
        var serializer Serializer

        switch config.Server.Format {
            case "JSON":
                serializer = JSONSerializer{}
            case "MessagePack":
                serializer = MsgPackSerializer{}
        }

        var raw interface{}
        ErrorLog.Printf("%s", req.Data)

        if err := serializer.Unmarshal([]byte(req.Data), &raw); err != nil {
            ErrorLog.Printf("%s", err)
            response, _ = serializer.Marshal(RPCResponse{Error: &RPCError{-32700, "Parse error"}})
        } else {
            // Определение, является ли raw массивом или объектом
            switch raw.(type) {
            case []interface{}:
                // Обрабатываем как батч запрос
                var requests []RPCRequest
                if err := serializer.Unmarshal([]byte(req.Data), &requests); err != nil {
                    response, _ = serializer.Marshal(RPCResponse{Error: &RPCError{-32600, "Invalid Batch Request"}})
                }

                var wg sync.WaitGroup
                responses := make([]RPCResponse, len(requests))

                // Создаем горутину для каждого запроса в батче
                for i, req := range requests {
                    wg.Add(1)
                    go func(index int, request RPCRequest) {
                        defer wg.Done()
                        responses[index] = *rpc.handle(request)
                    }(i, req)
                }

                wg.Wait()

                response, _ = serializer.Marshal(responses)

            case map[string]interface{}:
                // Обрабатываем как одиночный запрос
                var request RPCRequest
                if err := serializer.Unmarshal([]byte(req.Data), &request); err != nil {
                    response, _ = serializer.Marshal(RPCResponse{Error: &RPCError{-32600, "Invalid Request"}})
                } else {
                    response, _ = serializer.Marshal(rpc.handle(request))
                }
            }
        }

        req.Response(string(response))
    }
}